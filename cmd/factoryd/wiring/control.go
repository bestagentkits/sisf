package wiring

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/audit"
	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/rbac"
	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/identity"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// GitHub is what the control zone does on GitHub: read issues and the
// policy files, and show intake's decisions. github.RESTClient is one.
type GitHub interface {
	intake.IssueReader
	intake.IssueWriter
	FileReader
	// Reconciliation lists the issues that changed since its last poll.
	github.IssueSource
	// The actions of the surfaces open the sample issue and the catalog's
	// pull requests.
	CreateIssue(ctx context.Context, repo github.RepoRef, n github.NewIssue) (github.Issue, error)
	BranchHead(ctx context.Context, repo github.RepoRef, branch string) (core.CommitSHA, error)
	CommitToBranch(ctx context.Context, repo github.RepoRef, bc github.BranchCommit) (core.CommitSHA, error)
	CreatePullRequest(ctx context.Context, repo github.RepoRef, pr github.NewPullRequest) (github.PullRequest, error)
	// The issue's progress comment is edited in place.
	UpdateComment(ctx context.Context, repo github.RepoRef, commentID int64, body string) (github.Comment, error)
}

// ControlConfig is what the control zone is built from.
type ControlConfig struct {
	// Manifest is the install's factory.yaml. It names the repository and
	// the GitHub installation the webhook route accepts; the policy itself is
	// read from GitHub at the approved revision.
	Manifest *manifest.Manifest
	// Store is the operational store opened for the control zone.
	Store  *sqlstore.Store
	GitHub GitHub
	// WebhookSecret verifies deliveries; ApprovalKey signs approval decisions.
	WebhookSecret core.SecretValue
	ApprovalKey   ed25519.PrivateKey
	// ListenAddr and AllowNonLoopback configure the HTTP server.
	ListenAddr       string
	AllowNonLoopback bool
	// Probes check Zone 1 and Zone 2 for /healthz.
	Probes map[core.Zone]control.HealthProbe
	Clock  core.Clock
	// Logger receives operational messages, never a secret or a payload.
	Logger *slog.Logger
	// Upstream is the repository the factory's own repository is a fork
	// of; zero when there is none. Nothing is contributed to it unless the
	// Owner asks (see ContributeUpstream).
	Upstream github.RepoRef
	// TrustedProxies are where the edge connects from; see EnvTrustedProxies.
	TrustedProxies []netip.Prefix
	// Surfaces configures sign-in and the dashboard, REST, MCP and WebMCP
	// surfaces. Nil builds the zone without its HTTP server, for the
	// Owner's command-line use of the control zone.
	Surfaces *SurfaceConfig
	// Decision runs the decision layer on the edge's counters; nil runs none.
	Decision *DecisionConfig
	// Reconcile configures the polls that recover issue changes whose
	// webhooks were lost.
	Reconcile ReconcileConfig
}

// Control is the control zone of one factory: the HTTP server, intake, the
// approval store and the policy in force, and the passes that move
// deliveries in and effects out.
type Control struct {
	cfg ControlConfig
	log *slog.Logger
	// Server, Actions, Identity and Authorizer exist when the zone was
	// built with its surfaces.
	Server     *control.Server
	Actions    *actions.Service
	Identity   *auth.Service
	Authorizer *rbac.Authorizer
	Intake     *intake.Service
	Approvals  *approvals.KVStore
	Baselines  *policy.Baselines
	Policy     *policy.Loader
	Audit      *audit.Log

	mu sync.Mutex
	// published is the policy revision whose references were last published.
	published core.CommitSHA
	// backoff keeps deliveries whose processing failed from being retried on
	// every pass.
	backoff map[string]retryAt
	// projected is the record version last shown on GitHub, per issue.
	projected map[core.IssueRef]int64
	// progress writes the issues' progress comments; progressShown is the
	// body last written per issue, and redactor scrubs every body.
	progress      *github.ProgressComment
	progressShown map[core.IssueRef]string
	redactor      *redact.Redactor
	// decisions runs the decision layer; nil when it is not configured.
	decisions *decisionRunner
	// reconcile polls GitHub for the changes no webhook delivered; on an
	// install that takes no webhooks it is how issues arrive at all.
	reconcile *reconciler
	// access watches the operator token's write access on an install that
	// acts with it; nil on one with a GitHub App installation.
	access *operatorAccess
	// budget closes the reservation of a run whose issue the control zone
	// blocks because the run stopped.
	budget *budget.Gate
	// parkingDeferred is the earliest deadline of the unanswered issues
	// whose parking waits, and parkingDeferredWhy why; zero when none waits.
	parkingDeferred    time.Time
	parkingDeferredWhy string
}

// retryAt is when a failed delivery is tried again, and how often it failed.
type retryAt struct {
	at       time.Time
	failures int
}

// NewControl builds the control zone. Nothing runs until Run or Pass.
func NewControl(cfg ControlConfig) (*Control, error) {
	switch {
	case cfg.Manifest == nil || cfg.Store == nil || cfg.GitHub == nil:
		return nil, core.NewError(core.CodeInvalidArgument, "the control zone needs the manifest, a state store and a GitHub client")
	case len(cfg.ApprovalKey) != ed25519.PrivateKeySize:
		return nil, core.NewError(core.CodeInvalidArgument, "the control zone needs the approval signing key")
	case cfg.WebhookSecret.IsZero():
		return nil, missing(EnvWebhookSecret)
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	m := cfg.Manifest
	settings, err := GitHubSettingsOf(m)
	if err != nil {
		return nil, err
	}
	repo := github.RepoRef{Owner: m.Repository.Owner, Name: m.Repository.Repo}
	c := &Control{cfg: cfg, log: cfg.Logger, backoff: map[string]retryAt{}, projected: map[core.IssueRef]int64{},
		progress:      &github.ProgressComment{Issues: cfg.GitHub, Self: settings.Self, Factory: m.FactoryID},
		progressShown: map[core.IssueRef]string{}, redactor: redact.New(redact.Config{}),
		budget: budget.NewGate(budget.Config{Clock: cfg.Clock})}
	c.Audit = audit.New(cfg.Store, cfg.Clock)
	reconcile, err := cfg.Reconcile.withDefaults()
	if err != nil {
		return nil, err
	}
	if reconcile.Source == nil {
		reconcile.Source = cfg.GitHub
	}
	c.reconcile = newReconciler(reconcile, repo, settings.InstallationID, cfg.Store, cfg.Clock, c.confirmOwnEdit)

	signer, err := signing.NewSigner(cfg.ApprovalKey)
	if err != nil {
		return nil, err
	}
	verifier, err := signing.NewVerifier(cfg.ApprovalKey.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, err
	}
	if c.Approvals, err = approvals.NewStore(approvals.Config{State: cfg.Store, FactoryID: m.FactoryID,
		Signer: signer, Verifier: verifier, Audit: c.Audit, Clock: cfg.Clock}); err != nil {
		return nil, err
	}
	source, err := NewGitHubPolicySource(cfg.GitHub, repo)
	if err != nil {
		return nil, err
	}
	if c.Baselines, err = policy.NewBaselines(policy.BaselinesConfig{State: cfg.Store, Source: source,
		Approvals: c.Approvals, Audit: c.Audit, FactoryID: m.FactoryID, Clock: cfg.Clock}); err != nil {
		return nil, err
	}
	if c.Policy, err = policy.NewLoader(source, c.Baselines); err != nil {
		return nil, err
	}
	if c.Intake, err = intake.New(intake.Config{State: cfg.Store, Policy: c.Policy, Issues: cfg.GitHub,
		Approvals: c.Approvals, Clock: cfg.Clock}); err != nil {
		return nil, err
	}
	if cfg.Decision != nil {
		if cfg.Decision.Counters == nil {
			return nil, core.NewError(core.CodeInvalidArgument, "the decision layer needs the edge's counters")
		}
		c.decisions = &decisionRunner{cfg: *cfg.Decision, policy: c.Policy, issues: cfg.GitHub, repo: repo,
			store: cfg.Store, clock: cfg.Clock, log: cfg.Logger}
	}
	if cfg.Surfaces == nil {
		if settings.NoWebhooks {
			// The Owner's command-line use runs no passes; without the
			// token's confirmed access there is no installation to stamp
			// a reconciled change with.
			c.reconcile = nil
		}
		return c, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), identityTimeout)
	defer cancel()
	installation, err := c.installation(ctx, settings, cfg.Surfaces.GitHubToken, repo)
	if err != nil {
		return nil, err
	}
	// On an install that takes no webhooks the confirmed access stands for
	// the installation, so reconciled changes carry it as a delivery would.
	c.reconcile.installation = installation.ID
	if settings.NoWebhooks {
		c.access = newOperatorAccess(settings.APIURL, cfg.Surfaces.GitHubToken, installation, cfg.Clock)
	}
	surfaces, err := c.newSurfaces(ctx, *cfg.Surfaces, repo, installation)
	if err != nil {
		return nil, err
	}
	webhookSecret := cfg.WebhookSecret
	webhook := github.ReceiverConfig{
		Secrets:        func() []core.SecretValue { return []core.SecretValue{webhookSecret} },
		InstallationID: settings.InstallationID, Repos: []github.RepoRef{repo}, Self: settings.Self,
	}
	if settings.NoWebhooks {
		webhook = github.ReceiverConfig{}
	}
	if c.Server, err = control.New(control.Config{
		FactoryID: m.FactoryID, ListenAddr: cfg.ListenAddr, AllowNonLoopback: cfg.AllowNonLoopback,
		Webhook: webhook, NoWebhooks: settings.NoWebhooks,
	}, control.Deps{Store: cfg.Store, Probes: cfg.Probes, Clock: cfg.Clock, Logger: cfg.Logger, Surfaces: surfaces,
		// Reports are kept in the control zone's store, bounded by
		// control.DefaultMaxStoredReports and the report retention.
		Report: &control.ReportRoute{Redactor: redact.New(redact.Config{}), TrustedProxies: cfg.TrustedProxies},
	}); err != nil {
		return nil, err
	}
	return c, nil
}

// pruneReports removes stored bug reports past their retention. A prune
// that lost a race with a report being saved is left to the next pass.
func (c *Control) pruneReports(ctx context.Context) error {
	if err := c.Server.PruneReports(ctx); err != nil && !core.HasCode(err, core.CodeConflict) {
		return err
	}
	return nil
}

// installation is the factory's access to its repository, as the role
// authority checks it: the GitHub App installation the manifest names or,
// on an install that takes no webhooks, the access GitHub confirms the
// control zone's own token has.
func (c *Control) installation(ctx context.Context, s GitHubSettings, token core.SecretValue, repo github.RepoRef) (identity.Installation, error) {
	if !s.NoWebhooks {
		return identity.Installation{ID: s.InstallationID, Owner: repo.Owner, Repo: repo.Name}, nil
	}
	return operatorTokenAccess(ctx, s.APIURL, token, repo, &http.Client{Timeout: identityTimeout})
}

// Run serves the control zone on ln and runs a pass every interval until
// ctx ends. A failed pass is logged and the next one tries again.
func (c *Control) Run(ctx context.Context, ln net.Listener, interval time.Duration) error {
	if c.Server == nil {
		return core.NewError(core.CodeInvalidArgument, "this control zone was built without its surfaces and cannot serve")
	}
	served := make(chan error, 1)
	go func() { served <- c.Server.Serve(ctx, ln) }()
	for {
		if err := c.Pass(ctx); err != nil && ctx.Err() == nil {
			c.log.Warn("a control pass failed", "code", string(core.CodeOf(err)), "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return <-served
		case err := <-served:
			return err
		case <-c.cfg.Clock.After(interval):
		}
	}
}

// Pass does one round of the control zone's background work: it publishes
// the references of a newly approved policy, hands the stored webhook
// deliveries and the decided approvals to intake, performs the effects intake queued on GitHub and
// parks the issues whose wait for an answer is over, polling GitHub first
// when an answer may not be known yet, and blocks the issues whose run
// stopped without a candidate; when it is configured,
// the decision layer reads the edge's counters, and the stored bug reports
// past their retention are pruned, a bounded number per pass. Once per reconciliation
// interval it asks GitHub for the issue changes no webhook delivered. Each part runs even
// when another failed, except the parking: a pass whose drain of the inbox
// failed or held a delivery back parks nothing, since the delivery may be
// an answer. The error joins their failures.
//
// On an install acting with the operator's token, a pass whose check of
// the token finds its write access revoked does none of this but the
// prune of lapsed reports, and returns PERMISSION_DENIED.
func (c *Control) Pass(ctx context.Context) error {
	// The prune reads nothing on GitHub, so it runs whatever the token's access.
	pruned := c.pruneReports(ctx)
	if c.access != nil {
		if err := c.access.check(ctx); err != nil {
			return errors.Join(err, pruned)
		}
	}
	published := c.publishPolicy(ctx)
	drained := c.drainInbox(ctx)
	decided := c.reconcileDecided(ctx)
	delivered := c.deliverOutbox(ctx)
	var expired error
	if drained == nil {
		expired = c.expireClarifications(ctx)
	}
	return errors.Join(
		published,
		drained,
		decided,
		delivered,
		expired,
		// Before the progress, which then shows the block and why.
		c.blockStoppedRuns(ctx),
		// The progress first: an issue is closed on GitHub only after its
		// progress comment says how it ended.
		c.projectProgress(ctx),
		c.projectLifecycle(ctx),
		c.runDecisions(ctx),
		pruned,
		// Last, so a slow or failing GitHub delays nothing above; what it
		// finds is drained by the next pass.
		c.reconcileDue(ctx),
	)
}

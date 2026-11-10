package wiring

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/internal/audit"
	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// GitHub is what the control zone does on GitHub: read issues and the
// policy files, and show intake's decisions. github.RESTClient is one.
type GitHub interface {
	intake.IssueReader
	intake.IssueWriter
	FileReader
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
	// OwnerToken guards the API and the dashboard; WebhookSecret verifies
	// deliveries; ApprovalKey signs approval decisions.
	OwnerToken    core.SecretValue
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
}

// Control is the control zone of one factory: the HTTP server, intake, the
// approval store and the policy in force, and the passes that move
// deliveries in and effects out.
type Control struct {
	cfg       ControlConfig
	log       *slog.Logger
	Server    *control.Server
	Intake    *intake.Service
	Approvals *approvals.KVStore
	Baselines *policy.Baselines
	Policy    *policy.Loader
	Audit     *audit.Log

	mu sync.Mutex
	// published is the policy revision whose references were last published.
	published core.CommitSHA
	// backoff keeps deliveries whose processing failed from being retried on
	// every pass.
	backoff map[string]retryAt
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
	c := &Control{cfg: cfg, log: cfg.Logger, backoff: map[string]retryAt{}}
	c.Audit = audit.New(cfg.Store, cfg.Clock)

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
	webhookSecret := cfg.WebhookSecret
	if c.Server, err = control.New(control.Config{
		FactoryID: m.FactoryID, ListenAddr: cfg.ListenAddr, AllowNonLoopback: cfg.AllowNonLoopback, OwnerToken: cfg.OwnerToken,
		Webhook: github.ReceiverConfig{
			Secrets:        func() []core.SecretValue { return []core.SecretValue{webhookSecret} },
			InstallationID: settings.InstallationID, Repos: []github.RepoRef{repo}, Self: settings.Self,
		},
	}, control.Deps{Store: cfg.Store, Probes: cfg.Probes, Clock: cfg.Clock, Logger: cfg.Logger}); err != nil {
		return nil, err
	}
	return c, nil
}

// Run serves the control zone on ln and runs a pass every interval until
// ctx ends. A failed pass is logged and the next one tries again.
func (c *Control) Run(ctx context.Context, ln net.Listener, interval time.Duration) error {
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
// parks the issues whose wait for an answer is over. Each part runs even
// when another failed; the error joins their failures.
func (c *Control) Pass(ctx context.Context) error {
	return errors.Join(
		c.publishPolicy(ctx),
		c.drainInbox(ctx),
		c.reconcileDecided(ctx),
		c.deliverOutbox(ctx),
		c.expireClarifications(ctx),
	)
}

// publishPolicy publishes the references of the approved revision once.
func (c *Control) publishPolicy(ctx context.Context) error {
	baseline, err := c.Baselines.Current(ctx)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return nil // nothing approved yet: intake holds, and so does the agent zone
	case err != nil:
		return err
	}
	c.mu.Lock()
	done := c.published == baseline.Revision
	c.mu.Unlock()
	if done {
		return nil
	}
	snapshot, err := c.Policy.Current(ctx)
	if err != nil {
		return err
	}
	if err := PublishPolicyRefs(ctx, c.cfg.Store, snapshot); err != nil {
		return err
	}
	c.mu.Lock()
	c.published = snapshot.Revision
	c.mu.Unlock()
	return nil
}

// expireClarifications parks the issues nobody answered in time.
func (c *Control) expireClarifications(ctx context.Context) error {
	parked, err := c.Intake.ExpireClarifications(ctx)
	for _, issue := range parked {
		c.log.Info("parked an issue that was not answered in time", "issue", issue.String())
	}
	return err
}

// LocalOwner is the Owner acting from the factory's own host, through the
// factoryd command line. Running it needs the control zone's secrets, which
// only the Owner of the install can read; the identity was just proven by
// that access, so it counts as a fresh step-up.
func LocalOwner(clock core.Clock) core.Actor {
	return core.Actor{ID: "owner:local", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner},
		Surface: core.SurfaceCLI, StepUpAt: clock.Now()}
}

// ApproveBaseline makes revision the policy in force, as the Owner. The
// first revision needs nothing more; replacing one needs approvalID, an
// approval of exactly the new content.
func (c *Control) ApproveBaseline(ctx context.Context, revision core.CommitSHA, approvalID string) (policy.Baseline, error) {
	return c.Baselines.Approve(ctx, LocalOwner(c.cfg.Clock), revision, approvalID)
}

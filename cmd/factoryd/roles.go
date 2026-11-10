package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/cmd/supervisor/observe"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// defaultFakeAgentWork is how long a run of the scripted agent works before
// it leaves its patch. Long enough to watch a run in Developing, shorter
// than the default run timeout.
const defaultFakeAgentWork = 10 * time.Minute

// zoneSetup is what every role reads first: its environment, the manifest,
// and the state store opened for its zone.
type zoneSetup struct {
	env      wiring.Env
	manifest *manifest.Manifest
	store    *sqlstore.Store
	clock    core.Clock
}

// setup reads the environment and the manifest and opens the store for zone.
// The factory named by the environment, when it names one, must be the
// manifest's.
func setup(ctx context.Context, getenv func(string) string, zone core.Zone) (zoneSetup, error) {
	env, err := wiring.ReadEnv(getenv)
	if err != nil {
		return zoneSetup{}, err
	}
	m, err := manifest.Load(env.Config)
	if err != nil {
		return zoneSetup{}, err
	}
	if env.FactoryID != "" && env.FactoryID != m.FactoryID {
		return zoneSetup{}, core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("%s names factory %q but the manifest declares %q", wiring.EnvFactoryID, env.FactoryID, m.FactoryID))
	}
	clock := core.SystemClock{}
	store, err := wiring.OpenStore(ctx, env.StateDSN, m.FactoryID, zone, clock)
	if err != nil {
		return zoneSetup{}, err
	}
	return zoneSetup{env: env, manifest: m, store: store, clock: clock}, nil
}

// newControl builds the control zone from its setup; surfaces and decisions
// are nil for the Owner's command-line use, which serves and decides nothing.
func newControl(s zoneSetup, log *slog.Logger, surfaces *wiring.SurfaceConfig, decisions *wiring.DecisionConfig) (*wiring.Control, error) {
	settings, err := wiring.GitHubSettingsOf(s.manifest)
	if err != nil {
		return nil, err
	}
	if s.env.GitHubAPIURL != "" {
		settings.APIURL = s.env.GitHubAPIURL
	}
	client, err := wiring.NewGitHubClient(settings, s.env.GitHubToken, s.clock)
	if err != nil {
		return nil, err
	}
	key, err := wiring.ParseApprovalKey(s.env.ApprovalKey)
	if err != nil {
		return nil, err
	}
	return wiring.NewControl(wiring.ControlConfig{
		Manifest: s.manifest, Store: s.store, GitHub: client,
		WebhookSecret: s.env.WebhookSecret, ApprovalKey: key, Surfaces: surfaces, Decision: decisions,
		ListenAddr: listenOr(s.env.Listen, wiring.DefaultControlListen), AllowNonLoopback: s.env.AllowNonLoopback,
		Probes: wiring.ZoneProbes(s.env.AppHealthURL, s.env.DispatcherHealthURL), Clock: s.clock, Logger: log,
		TrustedProxies: s.env.TrustedProxies,
	})
}

func listenOr(addr, fallback string) string {
	if addr == "" {
		return fallback
	}
	return addr
}

// runControl serves the control zone until ctx ends.
func runControl(ctx context.Context, args []string, getenv func(string) string, proc *observe.Process) error {
	log := proc.Log
	if err := parse(newFlags("control"), args); err != nil {
		return err
	}
	s, err := setup(ctx, getenv, core.ZoneControl)
	if err != nil {
		return err
	}
	defer func() { _ = s.store.Close() }()
	surfaces, err := surfacesFromEnv(getenv, s, proc)
	if err != nil {
		return err
	}
	decisions, err := decisionsFromEnv(getenv)
	if err != nil {
		return err
	}
	c, err := newControl(s, log, surfaces, decisions)
	if err != nil {
		return err
	}
	ln, err := wiring.Listen(ctx, listenOr(s.env.Listen, wiring.DefaultControlListen), s.env.AllowNonLoopback)
	if err != nil {
		return err
	}
	log.Info("control plane listening", "factory", string(s.manifest.FactoryID), "addr", ln.Addr().String())
	return c.Run(ctx, ln, wiring.ControlPassInterval)
}

// newDispatcher builds the dispatcher with the scripted agent, the only
// agent backend there is so far.
func newDispatcher(s zoneSetup, work time.Duration) (*dispatcher.Dispatcher, error) {
	backend, err := fake.New(fake.Config{Clock: s.clock, Default: fake.Patch(work, "")})
	if err != nil {
		return nil, err
	}
	return wiring.NewDispatcher(wiring.DispatcherConfig{
		Manifest: s.manifest, Store: s.store, Agent: backend, Clock: s.clock,
		PollInterval: wiring.DispatcherPollInterval, LeaseTTL: wiring.DispatcherLeaseTTL,
	})
}

// runPolicyApprove makes a revision the policy in force, as the Owner.
func runPolicyApprove(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer, log *slog.Logger) error {
	fs := newFlags("policy approve")
	revision := fs.String("revision", "", "full commit SHA of the foundation files to put in force")
	approval := fs.String("approval", "", "approval request that approved this revision (needed to replace one in force)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *revision == "" {
		return fmt.Errorf("%w: --revision is required", errUsage)
	}
	s, err := setup(ctx, getenv, core.ZoneControl)
	if err != nil {
		return err
	}
	defer func() { _ = s.store.Close() }()
	c, err := newControl(s, log, nil, nil)
	if err != nil {
		return err
	}
	baseline, err := c.ApproveBaseline(ctx, core.CommitSHA(*revision), *approval)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "policy revision %s is in force (content %s)\n", baseline.Revision, baseline.Digest)
	return err
}

// runPolicyRequest files, as the Owner, the approval request for replacing
// the policy in force with a revision, and prints only its ID.
func runPolicyRequest(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer, log *slog.Logger) error {
	fs := newFlags("policy request")
	revision := fs.String("revision", "", "full commit SHA of the foundation files to put in force")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *revision == "" {
		return fmt.Errorf("%w: --revision is required", errUsage)
	}
	s, err := setup(ctx, getenv, core.ZoneControl)
	if err != nil {
		return err
	}
	defer func() { _ = s.store.Close() }()
	c, err := newControl(s, log, nil, nil)
	if err != nil {
		return err
	}
	request, err := c.RequestPolicyChange(ctx, core.CommitSHA(*revision))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, request.ID)
	return err
}

// runIssueRetry sends a Blocked issue back to Ready, as the Owner.
func runIssueRetry(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer, log *slog.Logger) error {
	fs := newFlags("issue retry")
	ref := fs.String("issue", "", "issue to retry, owner/repo#N")
	if err := parse(fs, args); err != nil {
		return err
	}
	issue, err := core.ParseIssueRef(*ref)
	if err != nil {
		return fmt.Errorf("%w: --issue must be owner/repo#N", errUsage)
	}
	s, err := setup(ctx, getenv, core.ZoneControl)
	if err != nil {
		return err
	}
	defer func() { _ = s.store.Close() }()
	c, err := newControl(s, log, nil, nil)
	if err != nil {
		return err
	}
	result, err := c.Retry(ctx, issue, wiring.LocalOwner(s.clock))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s is %s\n", issue, result.State)
	return err
}

// prList collects repeated --pr flags.
type prList []string

func (p *prList) String() string     { return fmt.Sprint([]string(*p)) }
func (p *prList) Set(v string) error { *p = append(*p, v); return nil }

// runReport records what a run produced, under the lease it holds. A report
// from anyone but the latest holder of a live lease is refused.
func runReport(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	fs := newFlags("report")
	ref := fs.String("issue", "", "issue of the run, owner/repo#N")
	run := fs.String("run", "", "run ID")
	owner := fs.String("owner", "", "lease holder the report is made under")
	token := fs.Int64("token", 0, "fencing token of that lease")
	var prs prList
	fs.Var(&prs, "pr", "pull request the run opened (repeatable)")
	if err := parse(fs, args); err != nil {
		return err
	}
	issue, err := core.ParseIssueRef(*ref)
	if err != nil {
		return fmt.Errorf("%w: --issue must be owner/repo#N", errUsage)
	}
	s, err := setup(ctx, getenv, core.ZoneAgent)
	if err != nil {
		return err
	}
	defer func() { _ = s.store.Close() }()
	d, err := newDispatcher(s, defaultFakeAgentWork)
	if err != nil {
		return err
	}
	err = d.Report(ctx, dispatcher.Report{Issue: issue, RunID: core.RunID(*run), LeaseOwner: *owner,
		FencingToken: core.FencingToken(*token), PullRequests: prs})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "report on run %s of %s recorded\n", *run, issue)
	return err
}

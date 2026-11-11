// Package harness runs a whole factory in one test process: the control
// zone and the dispatcher as the factoryd binary wires them, a fake GitHub
// that holds the repository and its foundation files, the scripted agent,
// a SQLite store in a temporary directory and a clock only the test moves.
//
// Nothing runs on its own. A test drives the factory with Pass (the control
// zone's background work) and Tick (one round of the dispatcher), so every
// interleaving it shows is one it chose. Webhooks are real HTTP deliveries
// from the fake to the control zone's handler, signed with the factory's
// secret, so they pass the same checks a delivery from GitHub passes.
package harness

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/cmd/factoryd/lifecycle"
	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/qa/agentjudge"
	"github.com/bestagentkits/sisf/internal/supervisor/killswitch"
	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
	"github.com/bestagentkits/sisf/internal/testkit/fakeoidc"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

// Start is the instant the harness clock starts at.
var Start = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

// DispatcherOwner is the lease holder name of the first harness
// dispatcher. Each restart takes a new name, as a restarted factoryd process
// does (see wiring.DispatcherOwner).
const DispatcherOwner = "dispatcher@harness#1"

// Poll interval and lease lifetime of the harness dispatcher.
const (
	PollInterval = 30 * time.Second
	LeaseTTL     = 2 * time.Minute
)

// Options change how the factory is built.
type Options struct {
	// Unconfirmed leaves the Owner's budget confirmation out, so the
	// dispatcher claims nothing.
	Unconfirmed bool
	// NoPolicy leaves the policy unapproved, so intake holds.
	NoPolicy bool
	// FactoryID gives the factory its own ID; with it the factory serves
	// its own repository, "<factory id>/hello-app".
	FactoryID core.FactoryID
	// ReleaseUnits replaces the manifest's release.units, the units the
	// Owner lets the factory publish by itself. The sample manifest names
	// the application alone.
	ReleaseUnits []manifest.UnitName
	// Upstream is a fake GitHub shared with other factories, where the
	// factory's repository is a fork of the upstream one. A factory gets a
	// fake GitHub of its own when it is nil.
	Upstream *Upstream
	// Decision runs the decision layer on every control pass that is due.
	Decision *wiring.DecisionConfig
	// Reconcile configures the control zone's polls of GitHub; zero values
	// take the defaults, polling the fake GitHub every DefaultReconcileInterval.
	Reconcile wiring.ReconcileConfig
	// Coder, when set, is the coding agent the dispatcher runs in place of
	// the scripted one, and CoderArtifacts where the agent zone's exporter
	// reads its patches.
	Coder          agent.Backend
	CoderArtifacts lifecycle.ArtifactSource
	// QAJudge, when set, builds the judge the supervisor's evaluator uses
	// in place of the scripted one, from the materials the supervisor
	// gathers of each candidate.
	QAJudge func(materials agentjudge.MaterialsSource) (qa.Judge, error)
	// OperatorToken makes the factory a development install that acts with
	// the operator's own gh login: it names no GitHub App installation,
	// takes no webhooks and learns of issues only by reconciliation.
	OperatorToken bool
}

// Factory is one factory running in the test process.
type Factory struct {
	t testing.TB

	Clock    *sqlstoretest.Clock
	Manifest *manifest.Manifest
	// Repo is the repository the factory serves, "owner/name".
	Repo string
	// PolicyRevision is the commit of the foundation files on the fake.
	PolicyRevision core.CommitSHA

	GitHub     *fakegithub.Server
	GitData    *fakegithub.GitData
	host       *Upstream
	Control    *wiring.Control
	Agent      *fake.Backend
	Dispatcher *dispatcher.Dispatcher

	// The lifecycle after development: the agent zone's exporter, the
	// supervisor's stages, the trust they rely on and the deployment.
	Exporter  *lifecycle.Exporter
	Stages    *stages.Stages
	Trust     *Trust
	Deploy    *Deployment
	Freeze    *killswitch.Switch
	Artifacts *lifecycle.MemoryArtifacts
	probes    *probes
	quiet     bool

	ControlStore *sqlstore.Store
	AgentStore   *sqlstore.Store

	// Identity is the fake identity provider people sign in at; it also
	// answers GitHub's sign-in and repository permission API.
	Identity *fakeoidc.Server
	// Supervisor stands for the release supervisor's admin socket.
	Supervisor *FakeSupervisor
	// Telemetry is what telemetry.search reads.
	Telemetry *telemetry.MemoryStore

	opts       Options
	dsn        string
	controlSrv *httptest.Server
	// owner is the Owner's browser, signed in on first use.
	ownerOnce sync.Once
	owner     *Person
	runs      atomic.Int64
	// dispatchers counts the dispatcher processes started, for their names.
	dispatchers atomic.Int64
}

// New builds and starts a factory and registers its teardown with t.
func New(t testing.TB, opts Options) *Factory {
	t.Helper()
	ctx := context.Background()
	root := moduleRoot(t)
	f := &Factory{t: t, opts: opts, Clock: sqlstoretest.NewClock(Start), dsn: filepath.Join(t.TempDir(), "factory.db"),
		Supervisor: &FakeSupervisor{}, Telemetry: telemetry.NewMemoryStore(1000)}

	manifestBytes := read(t, root, policy.PathFactory)
	if opts.FactoryID != "" {
		manifestBytes = forkManifest(manifestBytes, opts.FactoryID)
	}
	if len(opts.ReleaseUnits) > 0 {
		manifestBytes = releaseUnitsManifest(t, manifestBytes, opts.ReleaseUnits)
	}
	m, err := manifest.Parse(manifestBytes)
	if err != nil {
		t.Fatalf("harness: the sample manifest cannot be read: %v", err)
	}
	m.Extensions = map[string]any{"profile": "sim"}
	f.Manifest, f.Repo = m, m.Repository.Owner+"/"+m.Repository.Repo

	host := opts.Upstream
	if host == nil {
		host = newHost(t)
	}
	f.host, f.GitHub, f.GitData = host, host.GitHub, host.GitData
	if opts.OperatorToken {
		m.Mode = manifest.ModeStandalone
		m.Extensions = map[string]any{"profile": "default", "dev": true, "github_auth_via": "gh-cli", "github_api_url": host.URL}
	}
	if err := f.GitHub.AddRepo(f.Repo); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{policy.PathFactory: string(manifestBytes)}
	for _, path := range []string{policy.PathAgents, policy.PathReview, policy.PathDecisions, policy.PathFeatures} {
		files[path] = string(read(t, root, path))
	}
	for path, content := range SeedFiles {
		files[path] = content
	}
	sha, err := f.GitHub.Commit(f.Repo, files)
	if err != nil {
		t.Fatal(err)
	}
	f.PolicyRevision = core.CommitSHA(sha)
	if err := f.GitData.SetBranch(f.Repo, "main", sha); err != nil {
		t.Fatal(err)
	}

	bootstrap := install.BootstrapConfig{FactoryID: m.FactoryID, Dialect: sqlstore.DialectSQLite, DSN: f.dsn, Clock: f.Clock}
	if !opts.Unconfirmed {
		bootstrap.Budget = &install.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: f.Clock.Now()}
	}
	if err := install.Bootstrap(ctx, bootstrap); err != nil {
		t.Fatalf("harness: prepare the state store: %v", err)
	}
	f.ControlStore = f.openStore(core.ZoneControl)
	f.AgentStore = f.openStore(core.ZoneAgent)

	// The GitHub client waits out failures on the wall clock: the harness
	// clock moves only when the test moves it.
	client, err := wiring.NewGitHubClient(wiring.GitHubSettings{APIURL: host.URL}, core.NewSecretValue([]byte("harness-github-token")), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	webhookSecret := host.secret
	var upstream github.RepoRef
	if opts.Upstream != nil {
		owner, name, _ := strings.Cut(opts.Upstream.Repo, "/")
		upstream = github.RepoRef{Owner: owner, Name: name}
	}
	f.controlSrv = httptest.NewUnstartedServer(nil)
	base := "http://" + f.controlSrv.Listener.Addr().String()
	f.Control, err = wiring.NewControl(wiring.ControlConfig{
		Manifest: m, Store: f.ControlStore, GitHub: client,
		WebhookSecret: core.NewSecretValue([]byte(webhookSecret)), ApprovalKey: key, Clock: f.Clock, Upstream: upstream,
		Probes: map[core.Zone]control.HealthProbe{
			core.ZoneApplication: control.HealthProbeFunc(func(context.Context) error { return nil }),
			core.ZoneAgent:       control.HealthProbeFunc(f.dispatcherAlive),
		},
		Surfaces: f.surfaceConfig(base), Decision: opts.Decision, Reconcile: opts.Reconcile,
	})
	if err != nil {
		t.Fatalf("harness: build the control zone: %v", err)
	}
	f.controlSrv.Config.Handler = f.Control.Server.Handler()
	f.controlSrv.Start()
	t.Cleanup(f.controlSrv.Close)
	host.subscribe(f.controlSrv.URL + "/webhooks/github")

	if !opts.NoPolicy {
		if _, err := f.Control.ApproveBaseline(ctx, f.PolicyRevision, ""); err != nil {
			t.Fatalf("harness: approve the policy: %v", err)
		}
	}
	if f.Agent, err = fake.New(fake.Config{Clock: f.Clock, Roles: []agent.Role{agent.RoleCoder, agent.RoleQA}}); err != nil {
		t.Fatal(err)
	}
	f.Dispatcher = f.newDispatcher(f.AgentStore)
	f.startLifecycle(client)
	return f
}

// SeedFiles are the application and dashboard sources the fake repository
// starts with, next to the foundation files: what a scripted patch changes.
var SeedFiles = map[string]string{
	"examples/hello-app/main.go": "package main\n\nfunc main() { println(\"hello\") }\n",
	"ui/dashboard/status.html":   "<h1>Factory status</h1>\n<p>All systems nominal.</p>\n",
	"internal/qa/thresholds.txt": "pass_score=85\n",
}

// moduleRoot is the repository root, found from this file's location so a
// test in any package can build the factory.
func moduleRoot(t testing.TB) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("harness: cannot locate the module root")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

func read(t testing.TB, root, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("harness: read %s: %v", path, err)
	}
	return data
}

// token returns a random secret for one run of the harness.
func token(t testing.TB) string {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", raw)
}

// openStore opens the factory's store for zone, closed at the end of the test.
func (f *Factory) openStore(zone core.Zone) *sqlstore.Store {
	f.t.Helper()
	store, err := wiring.OpenStore(context.Background(), core.NewSecretValue([]byte(f.dsn)), f.Manifest.FactoryID, zone, f.Clock)
	if err != nil {
		f.t.Fatalf("harness: open the store for the %s zone: %v", zone, err)
	}
	f.t.Cleanup(func() { _ = store.Close() })
	return store
}

// newDispatcher builds a dispatcher on store with the harness's agent.
func (f *Factory) newDispatcher(store state.Store) *dispatcher.Dispatcher {
	f.t.Helper()
	d, err := wiring.NewDispatcher(wiring.DispatcherConfig{
		Manifest: f.Manifest, Store: store, Agent: f.coder(), Clock: f.Clock,
		Owner:        fmt.Sprintf("dispatcher@harness#%d", f.dispatchers.Add(1)),
		PollInterval: PollInterval, LeaseTTL: LeaseTTL,
		NewRunID: func() core.RunID { return core.RunID(fmt.Sprintf("run-%04d", f.runs.Add(1))) },
	})
	if err != nil {
		f.t.Fatalf("harness: build the dispatcher: %v", err)
	}
	return d
}

// coder is the coding agent the dispatcher runs.
func (f *Factory) coder() agent.Backend {
	if f.opts.Coder != nil {
		return f.opts.Coder
	}
	return f.Agent
}

// RestartDispatcher replaces the dispatcher by a new one under a new name,
// on a freshly opened store, as a killed and restarted process. The new one
// takes over once the leases of the old one lapse, LeaseTTL after the old
// one last ticked. The old one is returned so a test can show that what it
// still holds no longer counts.
func (f *Factory) RestartDispatcher() *dispatcher.Dispatcher {
	f.t.Helper()
	old := f.Dispatcher
	f.Dispatcher = f.newDispatcher(f.openStore(core.ZoneAgent))
	return old
}

// dispatcherAlive is the agent zone's probe: the dispatcher ticked.
func (f *Factory) dispatcherAlive(context.Context) error {
	if f.Dispatcher.Health().LastTick.IsZero() {
		return core.NewError(core.CodeUnavailable, "the dispatcher has not ticked yet")
	}
	return nil
}

// APIURL is the address of the fake GitHub's API the factory talks to.
func (f *Factory) APIURL() string { return f.host.URL }

// Pass runs one round of the control zone's background work and fails the
// test on an error.
func (f *Factory) Pass() {
	f.t.Helper()
	if err := f.Control.Pass(context.Background()); err != nil {
		f.t.Fatalf("harness: control pass: %v", err)
	}
}

// Tick moves the clock on by the poll interval and runs one dispatcher
// round, failing the test on an error.
func (f *Factory) Tick() dispatcher.TickResult {
	f.t.Helper()
	f.Clock.Advance(PollInterval)
	res, err := f.Dispatcher.Tick(context.Background())
	if err != nil {
		f.t.Fatalf("harness: dispatcher tick: %v", err)
	}
	if len(res.Errors) > 0 {
		f.t.Fatalf("harness: dispatcher tick reported errors: %v", res.Errors)
	}
	return res
}

// Settle runs control passes and dispatcher ticks, one after the other,
// rounds times.
func (f *Factory) Settle(rounds int) {
	f.t.Helper()
	for range rounds {
		f.Pass()
		f.Tick()
	}
}

// Issue names issue number of the served repository.
func (f *Factory) Issue(number int) core.IssueRef {
	return core.IssueRef{Owner: f.Manifest.Repository.Owner, Repo: f.Manifest.Repository.Repo, Number: number}
}

// OpenIssue opens an issue as login, whose relation to the repository is
// association (MEMBER, NONE, ...), and returns its number.
func (f *Factory) OpenIssue(login, association, title, body string, labels ...string) int {
	f.t.Helper()
	number, err := f.GitHub.OpenIssue(f.Repo, f.GitHub.Person(login, association), title, body, labels...)
	if err != nil {
		f.t.Fatalf("harness: open an issue: %v", err)
	}
	return number
}

// Comment posts a comment on issue number as login.
func (f *Factory) Comment(number int, login, association, body string) {
	f.t.Helper()
	if _, err := f.GitHub.AddComment(f.Repo, number, f.GitHub.Person(login, association), body); err != nil {
		f.t.Fatalf("harness: comment: %v", err)
	}
}

// Record returns the lifecycle record of issue number.
func (f *Factory) Record(number int) core.StateRecord {
	f.t.Helper()
	var record core.StateRecord
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		record, err = tx.GetRecord(ctx, f.Issue(number))
		return err
	})
	if err != nil {
		f.t.Fatalf("harness: read %s: %v", f.Issue(number), err)
	}
	return record
}

// Runs returns the run records the dispatcher stored for issue number.
func (f *Factory) Runs(number int) []dispatcher.RunRecord {
	f.t.Helper()
	var entries []state.KVEntry
	err := f.AgentStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		entries, err = tx.ListKV(ctx, "dispatcher", "run/", 0)
		return err
	})
	if err != nil {
		f.t.Fatalf("harness: list runs: %v", err)
	}
	var out []dispatcher.RunRecord
	for _, entry := range entries {
		var run dispatcher.RunRecord
		if err := json.Unmarshal(entry.Value, &run); err != nil {
			f.t.Fatalf("harness: read a run record: %v", err)
		}
		if run.Issue == f.Issue(number) {
			out = append(out, run)
		}
	}
	return out
}

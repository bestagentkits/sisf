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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
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
	Control    *wiring.Control
	Agent      *fake.Backend
	Dispatcher *dispatcher.Dispatcher

	ControlStore *sqlstore.Store
	AgentStore   *sqlstore.Store

	ownerToken string
	dsn        string
	controlSrv *httptest.Server
	runs       atomic.Int64
	// dispatchers counts the dispatcher processes started, for their names.
	dispatchers atomic.Int64
}

// New builds and starts a factory and registers its teardown with t.
func New(t testing.TB, opts Options) *Factory {
	t.Helper()
	ctx := context.Background()
	root := moduleRoot(t)
	f := &Factory{t: t, Clock: sqlstoretest.NewClock(Start), ownerToken: token(t), dsn: filepath.Join(t.TempDir(), "factory.db")}

	manifestBytes := read(t, root, policy.PathFactory)
	m, err := manifest.Parse(manifestBytes)
	if err != nil {
		t.Fatalf("harness: the sample manifest cannot be read: %v", err)
	}
	m.Extensions = map[string]any{"profile": "sim"}
	f.Manifest, f.Repo = m, m.Repository.Owner+"/"+m.Repository.Repo

	f.GitHub = fakegithub.New()
	if err := f.GitHub.AddRepo(f.Repo); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{policy.PathFactory: string(manifestBytes)}
	for _, path := range []string{policy.PathAgents, policy.PathReview, policy.PathDecisions, policy.PathFeatures} {
		files[path] = string(read(t, root, path))
	}
	sha, err := f.GitHub.Commit(f.Repo, files)
	if err != nil {
		t.Fatal(err)
	}
	f.PolicyRevision = core.CommitSHA(sha)
	githubSrv := httptest.NewServer(f.GitHub)
	t.Cleanup(githubSrv.Close)

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
	client, err := wiring.NewGitHubClient(wiring.GitHubSettings{APIURL: githubSrv.URL}, core.NewSecretValue([]byte("harness-github-token")), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	webhookSecret := token(t)
	f.Control, err = wiring.NewControl(wiring.ControlConfig{
		Manifest: m, Store: f.ControlStore, GitHub: client, OwnerToken: core.NewSecretValue([]byte(f.ownerToken)),
		WebhookSecret: core.NewSecretValue([]byte(webhookSecret)), ApprovalKey: key, Clock: f.Clock,
		Probes: map[core.Zone]control.HealthProbe{
			core.ZoneApplication: control.HealthProbeFunc(func(context.Context) error { return nil }),
			core.ZoneAgent:       control.HealthProbeFunc(f.dispatcherAlive),
		},
	})
	if err != nil {
		t.Fatalf("harness: build the control zone: %v", err)
	}
	f.controlSrv = httptest.NewServer(f.Control.Server.Handler())
	t.Cleanup(f.controlSrv.Close)
	f.GitHub.SetWebhook(f.controlSrv.URL+"/webhooks/github", webhookSecret)

	if !opts.NoPolicy {
		if _, err := f.Control.ApproveBaseline(ctx, f.PolicyRevision, ""); err != nil {
			t.Fatalf("harness: approve the policy: %v", err)
		}
	}
	if f.Agent, err = fake.New(fake.Config{Clock: f.Clock}); err != nil {
		t.Fatal(err)
	}
	f.Dispatcher = f.newDispatcher(f.AgentStore)
	return f
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
		Manifest: f.Manifest, Store: store, Agent: f.Agent, Clock: f.Clock,
		Owner:        fmt.Sprintf("dispatcher@harness#%d", f.dispatchers.Add(1)),
		PollInterval: PollInterval, LeaseTTL: LeaseTTL,
		NewRunID: func() core.RunID { return core.RunID(fmt.Sprintf("run-%04d", f.runs.Add(1))) },
	})
	if err != nil {
		f.t.Fatalf("harness: build the dispatcher: %v", err)
	}
	return d
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

// Get calls the control zone's API with the Owner token and decodes the
// JSON answer into out. It returns the HTTP status.
func (f *Factory) Get(path string, out any) int {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.controlSrv.URL+path, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.ownerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("harness: GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			f.t.Fatalf("harness: decode %s: %v", path, err)
		}
	}
	return resp.StatusCode
}

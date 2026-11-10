package dispatcher_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

const (
	sha          = core.CommitSHA("0123456789abcdef0123456789abcdef01234567")
	leaseTTL     = 2 * time.Minute
	pollInterval = 30 * time.Second
)

// fixture is one factory: a migrated store, a clock only the test moves and
// a scripted agent backend. The budget is unconfirmed until confirm is called.
type fixture struct {
	t     *testing.T
	db    *sqlstoretest.Database
	store *sqlstore.Store
	clock *sqlstoretest.Clock
	agent *fake.Backend
	runs  atomic.Int64
}

func newFixture(t *testing.T, db *sqlstoretest.Database) *fixture {
	t.Helper()
	backend, err := fake.New(fake.Config{Clock: db.Clock})
	if err != nil {
		t.Fatalf("fake agent: %v", err)
	}
	return &fixture{t: t, db: db, store: db.Open(t, core.ZoneAgent), clock: db.Clock, agent: backend}
}

// confirm stores the Owner's install-time budget confirmation where the
// installer writes it, through the control zone.
func (f *fixture) confirm() {
	f.t.Helper()
	raw, err := json.Marshal(dispatcher.BudgetConfirmation{
		Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: f.clock.Now().Add(-time.Hour),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	control := f.db.Open(f.t, core.ZoneControl)
	err = control.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		_, err := tx.PutKV(ctx, state.KVEntry{Namespace: dispatcher.ConfirmationNamespace, Key: dispatcher.ConfirmationKey, Value: raw})
		return err
	})
	if err != nil {
		f.t.Fatalf("store the budget confirmation: %v", err)
	}
}

func brief() dispatcher.Brief {
	return dispatcher.Brief{
		Handoff: agent.Handoff{SoW: "Add GET /version.", DoD: "GET /version returns the build revision."},
		Policy: core.PolicyRefs{PolicySHA: sha, ReviewSHA: sha, DecisionSHA: sha, FeatureCatalogSHA: sha,
			ModelRoute: core.ModelRoute{Provider: "anthropic", Model: "mid"}},
	}
}

// config is a dispatcher configuration for owner on the fixture's store.
func (f *fixture) config(owner string, store state.Store) dispatcher.Config {
	gate, err := dispatcher.NewConfirmedBudgetGate(dispatcher.KVConfirmation{}, f.clock)
	if err != nil {
		f.t.Fatalf("budget gate: %v", err)
	}
	return dispatcher.Config{
		Owner: owner, State: store, Agent: f.agent, Budget: gate,
		Briefs: dispatcher.BriefFunc(func(context.Context, core.IssueRef) (dispatcher.Brief, error) { return brief(), nil }),
		Limits: dispatcher.Limits{RunTimeout: time.Hour, MaxAttempts: 3, MaxConcurrent: 4},
		Clock:  f.clock, LeaseTTL: leaseTTL, PollInterval: pollInterval,
		NewRunID: func() core.RunID { return core.RunID(fmt.Sprintf("run-%04d", f.runs.Add(1))) },
	}
}

// dispatcher builds a dispatcher for owner, changed by adjust.
func (f *fixture) dispatcher(owner string, adjust func(*dispatcher.Config)) *dispatcher.Dispatcher {
	f.t.Helper()
	cfg := f.config(owner, f.store)
	if adjust != nil {
		adjust(&cfg)
	}
	d, err := dispatcher.New(cfg)
	if err != nil {
		f.t.Fatalf("new dispatcher: %v", err)
	}
	return d
}

// readyIssue takes a new issue through intake to Ready, as the control
// zone does.
func (f *fixture) readyIssue(number int) core.IssueRef {
	f.t.Helper()
	control := f.db.Open(f.t, core.ZoneControl)
	issue := core.IssueRef{Owner: "acme", Repo: "app", Number: number}
	steps := []core.Transition{
		{IssueID: issue, To: core.StateIntake, Event: core.EventIssueReceived, IdempotencyKey: "received"},
		{IssueID: issue, From: core.StateIntake, Event: core.EventInfoSufficient, ExpectedVersion: 1, IdempotencyKey: "sufficient"},
		{IssueID: issue, From: core.StateScopeDraft, Event: core.EventScopePermitted, ExpectedVersion: 2, IdempotencyKey: "permitted"},
	}
	for _, step := range steps {
		if !step.Creates() {
			to, err := statemachine.Next(step.From, step.Event)
			if err != nil {
				f.t.Fatal(err)
			}
			step.To = to
		}
		err := control.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
			_, err := tx.Transition(ctx, step)
			return err
		})
		if err != nil {
			f.t.Fatalf("take %s to %s: %v", issue, step.To, err)
		}
	}
	return issue
}

// record returns the lifecycle record of issue.
func (f *fixture) record(issue core.IssueRef) core.StateRecord {
	f.t.Helper()
	var record core.StateRecord
	err := f.store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		record, err = tx.GetRecord(ctx, issue)
		return err
	})
	if err != nil {
		f.t.Fatalf("read %s: %v", issue, err)
	}
	return record
}

// runRecords counts the run records the dispatcher stored for every issue.
func (f *fixture) runRecords() int {
	f.t.Helper()
	var entries []state.KVEntry
	err := f.store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		entries, err = tx.ListKV(ctx, "dispatcher", "run/", 0)
		return err
	})
	if err != nil {
		f.t.Fatalf("list run records: %v", err)
	}
	return len(entries)
}

// reservation reads one reservation.
func (f *fixture) reservation(id string) core.Reservation {
	f.t.Helper()
	var r core.Reservation
	err := f.store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		r, err = tx.GetReservation(ctx, id)
		return err
	})
	if err != nil {
		f.t.Fatalf("read reservation %s: %v", id, err)
	}
	return r
}

// issueLeaseIsFree reports whether another owner could take the issue lease
// now. It takes and gives the lease back in a transaction that rolls back.
func (f *fixture) issueLeaseIsFree(issue core.IssueRef) bool {
	f.t.Helper()
	errProbe := core.NewError(core.CodeInternal, "probe rolls back")
	err := f.store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		if _, err := tx.AcquireLease(ctx, state.IssueLeaseResource(issue), "probe", time.Second); err != nil {
			return err
		}
		return errProbe
	})
	switch {
	case err == errProbe:
		return true
	case core.HasCode(err, core.CodeLeaseHeld):
		return false
	}
	f.t.Fatalf("probe the lease on %s: %v", issue, err)
	return false
}

// runRecord reads the run record through d.
func runRecord(t *testing.T, d *dispatcher.Dispatcher, run core.RunID) dispatcher.RunRecord {
	t.Helper()
	record, err := d.RunRecord(context.Background(), run)
	if err != nil {
		t.Fatalf("read run %s: %v", run, err)
	}
	return record
}

// tick runs one tick and fails the test on a store failure or a run error.
func tick(t *testing.T, d *dispatcher.Dispatcher) dispatcher.TickResult {
	t.Helper()
	res, err := d.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(res.Errors) > 0 {
		t.Fatalf("tick reported errors: %v", res.Errors)
	}
	return res
}

// tickUntil advances the clock by the poll interval and ticks until done
// holds, for at most limit ticks.
func (f *fixture) tickUntil(d *dispatcher.Dispatcher, limit int, done func(dispatcher.TickResult) bool) dispatcher.TickResult {
	f.t.Helper()
	for range limit {
		f.clock.Advance(pollInterval)
		if res := tick(f.t, d); done(res) {
			return res
		}
	}
	f.t.Fatalf("the condition did not hold within %d ticks", limit)
	return dispatcher.TickResult{}
}

// tokenOf returns the fencing token d holds for run.
func tokenOf(t *testing.T, d *dispatcher.Dispatcher, run core.RunID) core.FencingToken {
	t.Helper()
	for _, info := range d.Health().Runs {
		if info.RunID == run {
			return info.FencingToken
		}
	}
	t.Fatalf("the dispatcher holds no lease for run %s", run)
	return 0
}

// patchArtifact is an artifact a worker might report.
func patchArtifact(name string) agent.Artifact {
	return agent.Artifact{Name: name, Kind: agent.ArtifactLog,
		Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", URI: "file:///" + name, Size: 3}
}

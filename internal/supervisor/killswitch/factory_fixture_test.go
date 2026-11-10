package killswitch

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/audit"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

const testFactory core.FactoryID = "acme-hello"

// fakeRuntime is the agent runtime and the secret broker of the factory: it
// records what it was asked to cancel and revoke, and can refuse.
type fakeRuntime struct {
	mu        sync.Mutex
	cancelled []agent.RunRef
	revoked   []core.RunID
	cancelErr error
	revokeErr error
}

func (f *fakeRuntime) Cancel(_ context.Context, run agent.RunRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancelErr != nil {
		return f.cancelErr
	}
	f.cancelled = append(f.cancelled, run)
	return nil
}

func (f *fakeRuntime) RevokeRun(_ context.Context, run core.RunID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revokeErr != nil {
		return f.revokeErr
	}
	f.revoked = append(f.revoked, run)
	return nil
}

// factory is one factory's store, seen by the control zone, which the
// supervisor opens it as, and by the agent zone, where the dispatcher runs.
type factory struct {
	t       *testing.T
	clock   *sqlstoretest.Clock
	control *sqlstore.Store
	agent   *sqlstore.Store
	runtime *fakeRuntime
	audit   *audit.Log
}

func newFactory(t *testing.T) *factory {
	t.Helper()
	return newFactoryAt(t, filepath.Join(t.TempDir(), "state.db"))
}

// newFactoryAt prepares the factory's store in the SQLite file dsn.
func newFactoryAt(t *testing.T, dsn string) *factory {
	t.Helper()
	ctx := context.Background()
	clock := sqlstoretest.NewClock(time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC))
	open := func(zone core.Zone) *sqlstore.Store {
		store, err := sqlstore.Open(ctx, sqlstore.Config{Dialect: sqlstore.DialectSQLite, DSN: dsn, FactoryID: testFactory, Zone: zone, Clock: clock})
		if err != nil {
			t.Fatalf("open the %s store: %v", zone, err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	f := &factory{t: t, clock: clock, control: open(core.ZoneControl), runtime: &fakeRuntime{}}
	if err := f.control.Migrate(ctx, state.MigrateOptions{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	f.agent = open(core.ZoneAgent)
	f.audit = audit.New(f.control, clock)
	return f
}

// kill returns the factory's switch.
func (f *factory) kill() *Switch {
	f.t.Helper()
	s, err := New(Config{FactoryID: testFactory, State: f.control, Clock: f.clock, Runs: f.runtime, Secrets: f.runtime, Audit: f.audit})
	if err != nil {
		f.t.Fatalf("New: %v", err)
	}
	return s
}

func issue(n int) core.IssueRef { return core.IssueRef{Owner: "acme", Repo: "hello", Number: n} }

// ready writes an issue through intake and scoping to Ready.
func (f *factory) ready(n int) core.StateRecord {
	f.t.Helper()
	var record core.StateRecord
	err := f.control.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		record, err = tx.Transition(ctx, core.Transition{IssueID: issue(n), To: core.StateIntake, Event: core.EventIssueReceived, IdempotencyKey: "intake/" + issue(n).String()})
		for _, step := range []struct {
			event core.Event
			to    core.State
		}{{core.EventInfoSufficient, core.StateScopeDraft}, {core.EventScopePermitted, core.StateReady}} {
			if err != nil {
				return err
			}
			record, err = tx.Transition(ctx, core.Transition{IssueID: issue(n), From: record.State, To: step.to, Event: step.event,
				ExpectedVersion: record.Version, IdempotencyKey: "seed/" + issue(n).String() + "/" + string(step.event)})
		}
		return err
	})
	if err != nil {
		f.t.Fatalf("seed issue %d to Ready: %v", n, err)
	}
	return record
}

// claim is the dispatcher claiming a Ready issue as owner; it returns the
// lease, or the error the store answered.
func (f *factory) claim(n int, owner string, ttl time.Duration) (state.Lease, error) {
	var lease state.Lease
	err := f.agent.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		lease, err = tx.AcquireLease(ctx, state.IssueLeaseResource(issue(n)), owner, ttl)
		return err
	})
	return lease, err
}

// developing has the dispatcher claim issue n for run under a lease of ttl,
// start development and record its agent run in flight.
func (f *factory) developing(n int, run core.RunID, ttl time.Duration) state.Lease {
	f.t.Helper()
	record := f.ready(n)
	lease, err := f.claim(n, "dispatcher-1", ttl)
	if err != nil {
		f.t.Fatalf("claim issue %d: %v", n, err)
	}
	err = f.agent.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		for _, step := range []struct {
			event core.Event
			to    core.State
		}{{core.EventClaimed, core.StateClaimed}, {core.EventDevelopmentStarted, core.StateDeveloping}} {
			key := "develop/" + string(run)
			if step.event == core.EventClaimed {
				key = "claim/" + string(run)
			}
			if record, err = tx.Transition(ctx, core.Transition{IssueID: issue(n), From: record.State, To: step.to, Event: step.event,
				ExpectedVersion: record.Version, IdempotencyKey: key, RunID: run, Attempt: 1,
				LeaseOwner: lease.Owner, FencingToken: lease.Token}); err != nil {
				return err
			}
		}
		raw, err := json.Marshal(dispatcher.RunRecord{
			Issue: issue(n), RunID: run, Status: dispatcher.RunWorking, Attempt: 1,
			Agent:     &agent.RunRef{Backend: "fake", ID: "agent-" + string(run), RunID: run},
			ClaimedAt: f.clock.Now(), UpdatedAt: f.clock.Now(),
		})
		if err != nil {
			return err
		}
		_, err = tx.PutKV(ctx, state.KVEntry{Namespace: "dispatcher", Key: "run/" + string(run), Value: raw})
		return err
	})
	if err != nil {
		f.t.Fatalf("issue %d to Developing: %v", n, err)
	}
	return lease
}

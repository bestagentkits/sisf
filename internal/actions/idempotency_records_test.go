package actions_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// openZoneStore opens the SQLite store at dsn for zone, migrated.
func openZoneStore(t *testing.T, dsn string, zone core.Zone, clock core.Clock) *sqlstore.Store {
	t.Helper()
	store, err := sqlstore.Open(t.Context(), sqlstore.Config{Dialect: sqlstore.DialectSQLite, DSN: dsn,
		FactoryID: testFactory, Zone: zone, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if zone == core.ZoneControl {
		if err := store.Migrate(t.Context(), state.MigrateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// keptIn is a service over f's parts that keeps idempotency records in
// records, as one control process does.
func keptIn(t *testing.T, f fixture, records actions.IdempotencyRecords) *actions.Service {
	t.Helper()
	reg := actions.NewRegistry()
	if err := reg.Register(sampleActions(f.widgets)...); err != nil {
		t.Fatal(err)
	}
	svc, err := actions.NewService(actions.Config{FactoryID: testFactory, Registry: reg, Authorizer: f.authz,
		Approvals: f.approvals, Policy: f.policy, Audit: f.audit, Clock: f.clock, StepUp: f.stepUps, Idempotency: records})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func renameWith(svc *actions.Service, actor core.Actor, in, key string) (core.ActionResult, error) {
	return svc.Invoke(context.Background(), "widgets.rename", core.ActionRequest{Actor: actor, Input: input(in), IdempotencyKey: key})
}

func TestTheControlStoreKeepsTheFirstAnswerAcrossARestart(t *testing.T) {
	f := newFixture(t)
	store := openZoneStore(t, filepath.Join(t.TempDir(), "state.db"), core.ZoneControl, f.clock)
	first, err := renameWith(keptIn(t, f, actions.NewKVIdempotency(store)), f.owner(), `{"id":"w1","name":"beta"}`, "k-1")
	if err != nil {
		t.Fatal(err)
	}
	restarted := keptIn(t, f, actions.NewKVIdempotency(store))
	again, err := renameWith(restarted, f.owner(), `{"id":"w1","name":"beta"}`, "k-1")
	if err != nil || again.AuditID != first.AuditID || f.applied() != 1 {
		t.Fatalf("after a restart: %v, %+v, %d writes", err, again, f.applied())
	}
	_, err = renameWith(restarted, f.owner(), `{"id":"w1","name":"gamma"}`, "k-1")
	requireCode(t, err, core.CodeConflict)
}

func TestTwoControlProcessesOnOneStoreRunAKeyedWriteOnce(t *testing.T) {
	f := newFixture(t)
	store := openZoneStore(t, filepath.Join(t.TempDir(), "state.db"), core.ZoneControl, f.clock)
	services := []*actions.Service{keptIn(t, f, actions.NewKVIdempotency(store)), keptIn(t, f, actions.NewKVIdempotency(store))}
	const callers = 12
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() { _, errs[i] = renameWith(services[i%2], f.owner(), `{"id":"w1","name":"beta"}`, "k-race") })
	}
	wg.Wait()
	for i, err := range errs {
		// A call that meets the key held by the other process's unfinished
		// call is told so rather than writing again.
		if err != nil && !core.HasCode(err, core.CodeConflict) {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if f.applied() != 1 {
		t.Fatalf("two processes wrote %d times for one key", f.applied())
	}
	if _, err := renameWith(services[1], f.owner(), `{"id":"w1","name":"beta"}`, "k-race"); err != nil {
		t.Fatalf("a retry once the first call finished: %v", err)
	}
}

func TestLapsedRecordsArePrunedFromTheStore(t *testing.T) {
	f := newFixture(t)
	store := openZoneStore(t, filepath.Join(t.TempDir(), "state.db"), core.ZoneControl, f.clock)
	svc := keptIn(t, f, actions.NewKVIdempotency(store))
	for _, key := range []string{"k-1", "k-2", "k-3"} {
		if _, err := renameWith(svc, f.owner(), `{"id":"w1","name":"beta"}`, key); err != nil {
			t.Fatal(err)
		}
	}
	if n := countRecords(t, store); n != 3 {
		t.Fatalf("%d records kept, want 3", n)
	}
	f.clock.Advance(2*actions.IdempotencyRetention + time.Hour)
	if _, err := renameWith(svc, f.owner(), `{"id":"w1","name":"gamma"}`, "k-4"); err != nil {
		t.Fatal(err)
	}
	if n := countRecords(t, store); n != 1 {
		t.Fatalf("%d records kept after the others lapsed, want 1", n)
	}
}

func TestNoOtherZoneReadsTheIdempotencyRecords(t *testing.T) {
	clock := newFixture(t).clock
	dsn := filepath.Join(t.TempDir(), "state.db")
	openZoneStore(t, dsn, core.ZoneControl, clock)
	agent := openZoneStore(t, dsn, core.ZoneAgent, clock)
	err := agent.View(t.Context(), func(ctx context.Context, tx state.Tx) error {
		_, err := tx.ListKV(ctx, actions.IdempotencyNamespace, "", 1)
		return err
	})
	if !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("the agent zone read the idempotency records: %v", err)
	}
}

func countRecords(t *testing.T, store state.Store) int {
	t.Helper()
	var n int
	err := store.View(t.Context(), func(ctx context.Context, tx state.Tx) error {
		entries, err := tx.ListKV(ctx, actions.IdempotencyNamespace, "", 1000)
		n = len(entries)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

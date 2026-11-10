package state_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

func TestIssueLeaseResourceIsStableAndDistinctPerIssue(t *testing.T) {
	a := core.IssueRef{Owner: "acme", Repo: "app", Number: 7}
	b := core.IssueRef{Owner: "acme", Repo: "app", Number: 8}
	if state.IssueLeaseResource(a) != "issue:acme/app#7" {
		t.Fatalf("IssueLeaseResource = %q", state.IssueLeaseResource(a))
	}
	if state.IssueLeaseResource(a) == state.IssueLeaseResource(b) {
		t.Fatal("two issues must not share a lease")
	}
}

func TestLeaseIsHeldOnlyBeforeItsExpiry(t *testing.T) {
	expires := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	lease := state.Lease{Resource: "dispatcher", Owner: "dispatcher-1", Token: 3, ExpiresAt: expires}
	if lease.Expired(expires.Add(-time.Nanosecond)) {
		t.Error("the lease is held just before its expiry")
	}
	if !lease.Expired(expires) || !lease.Expired(expires.Add(time.Hour)) {
		t.Error("the lease is lost at and after its expiry")
	}
	if !(state.Lease{}).Expired(expires) {
		t.Error("a lease that was never granted is not held")
	}
}

func TestMigrationsMustBeNumberedWithoutGaps(t *testing.T) {
	good := []state.Migration{
		{Version: 1, Name: "initial schema", BackwardCompatible: true},
		{Version: 2, Name: "add outbox retry columns", BackwardCompatible: true},
		{Version: 3, Name: "drop legacy column"},
	}
	if err := state.ValidateMigrations(good); err != nil {
		t.Fatalf("valid migrations rejected: %v", err)
	}
	if err := state.ValidateMigrations(nil); err != nil {
		t.Fatalf("an empty list is valid: %v", err)
	}
	for name, ms := range map[string][]state.Migration{
		"starts at zero": {{Version: 0, Name: "x"}},
		"gap":            {{Version: 1, Name: "a"}, {Version: 3, Name: "c"}},
		"repeat":         {{Version: 1, Name: "a"}, {Version: 1, Name: "b"}},
		"out of order":   {{Version: 2, Name: "b"}, {Version: 1, Name: "a"}},
		"unnamed":        {{Version: 1}},
	} {
		if core.CodeOf(state.ValidateMigrations(ms)) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, state.ValidateMigrations(ms))
		}
	}
	incompatible := state.Incompatible(good)
	if len(incompatible) != 1 || incompatible[0].Version != 3 {
		t.Fatalf("Incompatible = %+v", incompatible)
	}
	if len(state.Incompatible(good[:2])) != 0 {
		t.Fatal("compatible migrations must not be reported")
	}
}

// txStub proves a transaction can be implemented with the exported types
// alone and that it serves every narrow role, including the budget ledger.
type txStub struct{ deliveries map[string]bool }

func (txStub) GetRecord(context.Context, core.IssueRef) (core.StateRecord, error) {
	return core.StateRecord{}, core.NewError(core.CodeNotFound, "no record")
}
func (txStub) ListRecords(context.Context, state.RecordFilter) ([]core.StateRecord, error) {
	return nil, nil
}
func (txStub) Transition(_ context.Context, t core.Transition) (core.StateRecord, error) {
	if t.ExpectedVersion != 0 {
		return core.StateRecord{}, core.NewError(core.CodeConflict, "version moved on")
	}
	return core.StateRecord{IssueID: t.IssueID, State: t.To, Version: 1}, nil
}
func (txStub) AcquireLease(_ context.Context, resource, owner string, _ time.Duration) (state.Lease, error) {
	return state.Lease{Resource: resource, Owner: owner, Token: 1}, nil
}
func (txStub) RenewLease(_ context.Context, l state.Lease, _ time.Duration) (state.Lease, error) {
	return l, nil
}
func (txStub) ReleaseLease(context.Context, state.Lease) error { return nil }
func (txStub) CheckFencing(_ context.Context, _ string, token core.FencingToken) error {
	if token < 2 {
		return core.NewError(core.CodeStaleFencingToken, "a newer holder exists")
	}
	return nil
}
func (s txStub) AcceptDelivery(_ context.Context, e state.InboxEntry) (bool, error) {
	key := e.Source + "/" + e.DeliveryID
	if s.deliveries[key] {
		return false, nil
	}
	s.deliveries[key] = true
	return true, nil
}
func (txStub) PendingDeliveries(context.Context, int) ([]state.InboxEntry, error) { return nil, nil }
func (txStub) MarkDeliveryProcessed(context.Context, string, string) error        { return nil }
func (txStub) Enqueue(context.Context, state.OutboxMessage) error                 { return nil }
func (txStub) PendingOutbox(context.Context, int) ([]state.OutboxMessage, error)  { return nil, nil }
func (txStub) MarkOutboxSent(context.Context, int64) error                        { return nil }
func (txStub) MarkOutboxFailed(context.Context, int64, time.Time, string) error   { return nil }
func (txStub) PutReservation(context.Context, core.Reservation) error             { return nil }
func (txStub) GetReservation(context.Context, string) (core.Reservation, error) {
	return core.Reservation{}, core.NewError(core.CodeNotFound, "no reservation")
}
func (txStub) AppendUsage(context.Context, core.Usage) error { return nil }
func (txStub) BudgetTotals(context.Context, time.Time) (core.BudgetTotals, error) {
	return core.BudgetTotals{}, nil
}
func (txStub) GetKV(context.Context, string, string) (state.KVEntry, error) {
	return state.KVEntry{}, core.NewError(core.CodeNotFound, "no entry")
}
func (txStub) PutKV(_ context.Context, e state.KVEntry) (state.KVEntry, error) {
	e.Version++
	return e, nil
}
func (txStub) ListKV(context.Context, string, string, int) ([]state.KVEntry, error) { return nil, nil }
func (txStub) DeleteKV(context.Context, string, string, int64) error                { return nil }

type storeStub struct{ tx txStub }

func (s storeStub) Update(ctx context.Context, fn func(context.Context, state.Tx) error) error {
	return fn(ctx, s.tx)
}
func (s storeStub) View(ctx context.Context, fn func(context.Context, state.Tx) error) error {
	return fn(ctx, s.tx)
}
func (storeStub) Close() error { return nil }

func TestTransactionServesEveryNarrowRole(t *testing.T) {
	var store state.Store = storeStub{tx: txStub{deliveries: map[string]bool{}}}
	issue := core.IssueRef{Owner: "acme", Repo: "app", Number: 7}
	err := store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var (
			records state.Records     = tx
			leases  state.Leases      = tx
			inbox   state.Inbox       = tx
			_       state.Outbox      = tx
			_       state.KV          = tx
			_       core.BudgetLedger = tx
		)
		first, err := inbox.AcceptDelivery(ctx, state.InboxEntry{Source: "github", DeliveryID: "d-1"})
		if err != nil || !first {
			t.Errorf("first delivery = %v, %v", first, err)
		}
		again, err := inbox.AcceptDelivery(ctx, state.InboxEntry{Source: "github", DeliveryID: "d-1"})
		if err != nil || again {
			t.Errorf("a repeated delivery must be reported as already seen, got %v, %v", again, err)
		}
		lease, err := leases.AcquireLease(ctx, state.IssueLeaseResource(issue), "dispatcher-1", time.Minute)
		if err != nil {
			return err
		}
		if err := leases.CheckFencing(ctx, lease.Resource, lease.Token); !core.HasCode(err, core.CodeStaleFencingToken) {
			t.Errorf("a superseded holder must be fenced off, got %v", err)
		}
		_, err = records.Transition(ctx, core.Transition{IssueID: issue, To: core.StateIntake, ExpectedVersion: 4})
		return err
	})
	if !core.HasCode(err, core.CodeConflict) {
		t.Fatalf("a stale version must surface as CONFLICT, got %v", err)
	}
	var _ state.Migrator = migratorStub{}
}

type migratorStub struct{}

func (migratorStub) SchemaVersion(context.Context) (int, error)         { return 0, nil }
func (migratorStub) Pending(context.Context) ([]state.Migration, error) { return nil, nil }
func (migratorStub) Migrate(context.Context, state.MigrateOptions) error {
	return core.NewError(core.CodeApprovalRequired, "a migration that is not backward compatible needs the Owner")
}

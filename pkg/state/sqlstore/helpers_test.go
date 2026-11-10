package sqlstore_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

var ctx = context.Background()

// issue returns a distinct issue of the repository the tests work on.
func issue(number int) core.IssueRef {
	return core.IssueRef{Owner: "acme", Repo: "app", Number: number}
}

// update runs fn in one read-write transaction and returns its outcome.
func update(s *sqlstore.Store, fn func(tx state.Tx) error) error {
	return s.Update(ctx, func(_ context.Context, tx state.Tx) error { return fn(tx) })
}

// view runs fn in one read-only transaction and returns its outcome.
func view(s *sqlstore.Store, fn func(tx state.Tx) error) error {
	return s.View(ctx, func(_ context.Context, tx state.Tx) error { return fn(tx) })
}

// mustUpdate is update for steps a test takes for granted.
func mustUpdate(t *testing.T, s *sqlstore.Store, fn func(tx state.Tx) error) {
	t.Helper()
	if err := update(s, fn); err != nil {
		t.Fatalf("update: %v", err)
	}
}

// wantCode fails the test unless err carries exactly code.
func wantCode(t *testing.T, err error, code core.ErrorCode) {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("want %s, got %v", code, err)
	}
}

// received is the write that creates the record of an issue.
func received(ref core.IssueRef) core.Transition {
	return core.Transition{
		IssueID: ref, To: core.StateIntake, Event: core.EventIssueReceived,
		IdempotencyKey: "received", ActorID: "intake",
	}
}

// step is a write that moves an existing record along one lifecycle edge.
func step(ref core.IssueRef, from core.State, event core.Event, to core.State, version int64, key string) core.Transition {
	return core.Transition{
		IssueID: ref, From: from, Event: event, To: to,
		ExpectedVersion: version, IdempotencyKey: key, ActorID: "intake",
	}
}

// transition applies one write in a transaction of its own.
func transition(s *sqlstore.Store, t core.Transition) (core.StateRecord, error) {
	var record core.StateRecord
	err := update(s, func(tx state.Tx) (err error) {
		record, err = tx.Transition(ctx, t)
		return err
	})
	return record, err
}

// mustTransition is transition for writes a test takes for granted.
func mustTransition(t *testing.T, s *sqlstore.Store, tr core.Transition) core.StateRecord {
	t.Helper()
	record, err := transition(s, tr)
	if err != nil {
		t.Fatalf("transition %s: %v", tr.Event, err)
	}
	return record
}

// record reads the stored record of an issue.
func record(t *testing.T, s *sqlstore.Store, ref core.IssueRef) core.StateRecord {
	t.Helper()
	var got core.StateRecord
	if err := view(s, func(tx state.Tx) (err error) {
		got, err = tx.GetRecord(ctx, ref)
		return err
	}); err != nil {
		t.Fatalf("read record %s: %v", ref, err)
	}
	return got
}

// readyRecord drives an issue to Ready, the state a dispatcher claims from.
func readyRecord(t *testing.T, s *sqlstore.Store, ref core.IssueRef) core.StateRecord {
	t.Helper()
	mustTransition(t, s, received(ref))
	mustTransition(t, s, step(ref, core.StateIntake, core.EventInfoSufficient, core.StateScopeDraft, 1, "scoped"))
	return mustTransition(t, s, step(ref, core.StateScopeDraft, core.EventScopePermitted, core.StateReady, 2, "permitted"))
}

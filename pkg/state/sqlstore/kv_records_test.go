package sqlstore_test

import (
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// put writes one document in a transaction of its own.
func put(s *sqlstore.Store, e state.KVEntry) (state.KVEntry, error) {
	var written state.KVEntry
	err := update(s, func(tx state.Tx) (err error) {
		written, err = tx.PutKV(ctx, e)
		return err
	})
	return written, err
}

// keys lists the keys of a namespace that start with prefix.
func keys(t *testing.T, s *sqlstore.Store, namespace, prefix string, limit int) []string {
	t.Helper()
	var found []string
	if err := view(s, func(tx state.Tx) error {
		entries, err := tx.ListKV(ctx, namespace, prefix, limit)
		for _, e := range entries {
			found = append(found, e.Key)
		}
		return err
	}); err != nil {
		t.Fatalf("list %s/%s: %v", namespace, prefix, err)
	}
	return found
}

func TestDocumentIsWrittenOnlyAgainstTheVersionThatWasRead(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		created, err := put(s, state.KVEntry{Namespace: "approvals", Key: "a-1", Value: []byte("pending")})
		if err != nil || created.Version != 1 || !created.UpdatedAt.Equal(db.Clock.Now()) {
			t.Fatalf("created = %+v, %v", created, err)
		}
		// Creating over an existing document and writing from a stale read both lose.
		_, err = put(s, state.KVEntry{Namespace: "approvals", Key: "a-1", Value: []byte("forged")})
		wantCode(t, err, core.CodeConflict)
		updated, err := put(s, state.KVEntry{Namespace: "approvals", Key: "a-1", Value: []byte("approved"), Version: 1})
		if err != nil || updated.Version != 2 {
			t.Fatalf("updated = %+v, %v", updated, err)
		}
		_, err = put(s, state.KVEntry{Namespace: "approvals", Key: "a-1", Value: []byte("late"), Version: 1})
		wantCode(t, err, core.CodeConflict)
		// A version for a document that does not exist is stale too.
		_, err = put(s, state.KVEntry{Namespace: "approvals", Key: "a-2", Value: []byte("x"), Version: 3})
		wantCode(t, err, core.CodeConflict)

		var stored state.KVEntry
		if err := view(s, func(tx state.Tx) (err error) {
			stored, err = tx.GetKV(ctx, "approvals", "a-1")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if string(stored.Value) != "approved" || stored.Version != 2 || stored.Namespace != "approvals" || stored.Key != "a-1" {
			t.Fatalf("stored = %+v", stored)
		}

		wantCode(t, update(s, func(tx state.Tx) error { return tx.DeleteKV(ctx, "approvals", "a-1", 1) }), core.CodeConflict)
		wantCode(t, update(s, func(tx state.Tx) error { return tx.DeleteKV(ctx, "approvals", "missing", 1) }), core.CodeNotFound)
		mustUpdate(t, s, func(tx state.Tx) error { return tx.DeleteKV(ctx, "approvals", "a-1", 2) })
		wantCode(t, view(s, func(tx state.Tx) error {
			_, err := tx.GetKV(ctx, "approvals", "a-1")
			return err
		}), core.CodeNotFound)

		for name, e := range map[string]state.KVEntry{
			"no namespace":     {Key: "k"},
			"no key":           {Namespace: "approvals"},
			"negative version": {Namespace: "approvals", Key: "k", Version: -1},
		} {
			if _, err := put(s, e); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
			}
		}
	})
}

func TestDocumentsAreListedByExactPrefixInKeyOrder(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		for _, key := range []string{"sow/7/v2", "sow/7/v1", "sow/8/v1", "SOW/7/v1", "sow_7", "so%", "other"} {
			if _, err := put(s, state.KVEntry{Namespace: "intake", Key: key, Value: []byte(key)}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := put(s, state.KVEntry{Namespace: "policy", Key: "sow/7/v9"}); err != nil {
			t.Fatal(err)
		}
		if got := keys(t, s, "intake", "sow/7/", 0); len(got) != 2 || got[0] != "sow/7/v1" || got[1] != "sow/7/v2" {
			t.Fatalf("keys under sow/7/ = %v", got)
		}
		// Wildcard characters in a prefix match only themselves, and case matters.
		if got := keys(t, s, "intake", "so%", 0); len(got) != 1 || got[0] != "so%" {
			t.Fatalf("keys under so%% = %v", got)
		}
		if got := keys(t, s, "intake", "sow_", 0); len(got) != 1 || got[0] != "sow_7" {
			t.Fatalf("keys under sow_ = %v", got)
		}
		if got := keys(t, s, "intake", "", 0); len(got) != 7 || got[0] != "SOW/7/v1" {
			t.Fatalf("all keys of the namespace = %v", got)
		}
		if got := keys(t, s, "intake", "sow/", 2); len(got) != 2 || got[1] != "sow/7/v2" {
			t.Fatalf("keys under sow/ limited to 2 = %v", got)
		}
	})
}

func TestRecordsAreReadByIssueAndListedByStateAndTime(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		wantCode(t, view(s, func(tx state.Tx) error {
			_, err := tx.GetRecord(ctx, issue(7))
			return err
		}), core.CodeNotFound)

		mustTransition(t, s, received(issue(1)))
		db.Clock.Advance(time.Minute)
		afterFirst := db.Clock.Now()
		readyRecord(t, s, issue(2))
		db.Clock.Advance(time.Minute)
		mustTransition(t, s, received(issue(3)))

		list := func(filter state.RecordFilter) []int {
			var numbers []int
			if err := view(s, func(tx state.Tx) error {
				records, err := tx.ListRecords(ctx, filter)
				for _, r := range records {
					numbers = append(numbers, r.IssueID.Number)
				}
				return err
			}); err != nil {
				t.Fatalf("list records: %v", err)
			}
			return numbers
		}
		if got := list(state.RecordFilter{}); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
			t.Fatalf("all records, oldest write first = %v", got)
		}
		if got := list(state.RecordFilter{States: []core.State{core.StateReady}}); len(got) != 1 || got[0] != 2 {
			t.Fatalf("records in Ready = %v", got)
		}
		if got := list(state.RecordFilter{States: []core.State{core.StateIntake, core.StateReady}, Limit: 2}); len(got) != 2 {
			t.Fatalf("records in Intake or Ready limited to 2 = %v", got)
		}
		if got := list(state.RecordFilter{UpdatedSince: afterFirst}); len(got) != 2 || got[0] != 2 {
			t.Fatalf("records written since the second instant = %v", got)
		}
		if got := list(state.RecordFilter{States: []core.State{core.StateClosed}}); len(got) != 0 {
			t.Fatalf("records in Closed = %v", got)
		}
	})
}

package audit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// The log must satisfy the frozen contracts as they are written.
var (
	_ core.AuditLog    = (*Log)(nil)
	_ core.AuditReader = (*Log)(nil)
)

func TestAppendChainsEachRecordToTheOneBefore(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		log := openLog(t, db)
		// Whatever the caller puts in the chain fields is ignored.
		claimed := approval(1)
		claimed.Seq, claimed.PrevHash, claimed.Hash = 41, "made-up", "made-up"
		first, err := log.Append(ctx, claimed)
		if err != nil {
			t.Fatal(err)
		}
		if first.Seq != 1 || first.PrevHash != "" || len(first.Hash) != 64 || first.Hash == "made-up" {
			t.Fatalf("first record chain fields = %d, %q, %q", first.Seq, first.PrevHash, first.Hash)
		}
		if first.FactoryID != sqlstoretest.Factory || !first.At.Equal(db.Clock.Now()) {
			t.Fatalf("first record factory and time = %s, %s", first.FactoryID, first.At)
		}

		db.Clock.Advance(time.Minute)
		second, err := log.Append(ctx, approval(2))
		if err != nil {
			t.Fatal(err)
		}
		if second.Seq != 2 || second.PrevHash != first.Hash || second.Hash == first.Hash {
			t.Fatalf("second record chain fields = %d, %q, %q", second.Seq, second.PrevHash, second.Hash)
		}
	})
}

func TestEventsAreReadBackInOrderAndInPages(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		log := openLog(t, db)
		written := appendEvents(t, log, 5)

		all, err := log.Events(ctx, 0, 0)
		if err != nil || len(all) != 5 {
			t.Fatalf("all events = %d, %v", len(all), err)
		}
		for i, e := range all {
			w := written[i]
			if e.Seq != w.Seq || e.Hash != w.Hash || e.PrevHash != w.PrevHash || e.Action != w.Action ||
				e.IssueID != w.IssueID || e.Details["scope"] != "kind:feature" || !e.At.Equal(w.At) ||
				e.SubjectDigest != w.SubjectDigest || e.PolicySHA != w.PolicySHA || e.Outcome != w.Outcome {
				t.Fatalf("event %d read back as %+v, written as %+v", i+1, e, w)
			}
		}
		page, err := log.Events(ctx, 2, 2)
		if err != nil || len(page) != 2 || page[0].Seq != 3 || page[1].Seq != 4 {
			t.Fatalf("two events after the second = %+v, %v", page, err)
		}
		if tail, err := log.Events(ctx, 5, 10); err != nil || len(tail) != 0 {
			t.Fatalf("events after the last = %+v, %v", tail, err)
		}
	})
}

func TestMalformedEventIsRefusedAndLeavesTheChainUntouched(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		log := openLog(t, db)
		appendEvents(t, log, 1)
		for name, change := range map[string]func(*core.AuditEvent){
			"no actor":          func(e *core.AuditEvent) { e.ActorID = "" },
			"unknown actor":     func(e *core.AuditEvent) { e.ActorKind = "robot" },
			"no action":         func(e *core.AuditEvent) { e.Action = "" },
			"unknown outcome":   func(e *core.AuditEvent) { e.Outcome = "maybe" },
			"unknown surface":   func(e *core.AuditEvent) { e.Surface = "telnet" },
			"other factory":     func(e *core.AuditEvent) { e.FactoryID = "another-factory" },
			"malformed issue":   func(e *core.AuditEvent) { e.IssueID = core.IssueRef{Owner: "acme"} },
			"abbreviated sha":   func(e *core.AuditEvent) { e.PolicySHA = "1111111" },
			"mutable reference": func(e *core.AuditEvent) { e.SubjectDigest = "latest" },
			"malformed run":     func(e *core.AuditEvent) { e.RunID = "Run 1" },
		} {
			e := approval(2)
			change(&e)
			if _, err := log.Append(ctx, e); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
			}
		}
		if events, err := log.Events(ctx, 0, 0); err != nil || len(events) != 1 {
			t.Fatalf("events after the refusals = %d, %v", len(events), err)
		}
		if err := log.Verify(ctx); err != nil {
			t.Fatalf("refused events must leave the chain intact: %v", err)
		}
	})
}

func TestConcurrentAppendsFormOneUnbrokenChain(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		// The control plane and the dispatcher write to the same log.
		logs := []*Log{openLog(t, db), New(db.Open(t, core.ZoneAgent), db.Clock)}
		const writers, each = 6, 5
		var wg sync.WaitGroup
		errs := make(chan error, writers*each)
		for w := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range each {
					if _, err := logs[w%2].Append(ctx, approval(w*each+i+1)); err != nil {
						errs <- err
					}
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("concurrent append: %v", err)
		}
		events, err := logs[0].Events(ctx, 0, 0)
		if err != nil || len(events) != writers*each {
			t.Fatalf("events = %d, %v; want %d", len(events), err, writers*each)
		}
		for i, e := range events {
			if e.Seq != int64(i+1) {
				t.Fatalf("record %d has sequence %d; the chain must have no gap", i+1, e.Seq)
			}
		}
		if err := logs[1].Verify(ctx); err != nil {
			t.Fatalf("the chain written concurrently must verify: %v", err)
		}
	})
}

func TestEachFactoryHasAChainOfItsOwn(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ours := openLog(t, db)
		theirs := New(db.OpenFactory(t, "another-factory", core.ZoneControl), db.Clock)
		appendEvents(t, ours, 3)
		first, err := theirs.Append(ctx, approval(1))
		if err != nil || first.Seq != 1 || first.PrevHash != "" || first.FactoryID != "another-factory" {
			t.Fatalf("their first record = %+v, %v", first, err)
		}
		if events, err := theirs.Events(ctx, 0, 0); err != nil || len(events) != 1 {
			t.Fatalf("their events = %d, %v", len(events), err)
		}
		for _, log := range []*Log{ours, theirs} {
			if err := log.Verify(ctx); err != nil {
				t.Fatalf("each chain must verify on its own: %v", err)
			}
		}
	})
}

func TestApplicationZoneCanNeitherWriteNorReadTheAuditLog(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		appendEvents(t, openLog(t, db), 2)
		application := New(db.Open(t, core.ZoneApplication), db.Clock)
		_, err := application.Append(ctx, approval(3))
		wantCode(t, err, core.CodePermissionDenied)
		_, err = application.Events(ctx, 0, 0)
		wantCode(t, err, core.CodePermissionDenied)
		wantCode(t, application.Verify(ctx), core.CodePermissionDenied)
	})
}

func TestAppendInsideAStoreTransactionIsRefusedInsteadOfHanging(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		store := db.Open(t, core.ZoneControl)
		log := New(store, db.Clock)
		err := store.Update(ctx, func(inner context.Context, _ state.Tx) error {
			_, err := log.Append(inner, approval(1))
			return err
		})
		wantCode(t, err, core.CodeInvalidArgument)
		if events, err := log.Events(ctx, 0, 0); err != nil || len(events) != 0 {
			t.Fatalf("events after the refused append = %d, %v", len(events), err)
		}
		// Outside the transaction the same event is written.
		if _, err := log.Append(ctx, approval(1)); err != nil {
			t.Fatal(err)
		}
	})
}

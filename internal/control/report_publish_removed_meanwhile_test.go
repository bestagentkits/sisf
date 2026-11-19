package control_test

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// removedMeanwhile is a store on which another publisher writes the first
// document under prefix that this one is about to remove or write, between
// its read and its write. A removal then finds nothing, as a versioned
// delete does on PostgreSQL at read committed when the other transaction
// commits first; a write finds the document moved on.
type removedMeanwhile struct {
	state.Store
	prefix string
	// removalsOnly races only a removal, leaving the writes under prefix
	// that come before it alone.
	removalsOnly bool
	armed        atomic.Bool
}

// meanwhile arms a store over s that races the first write under prefix.
func meanwhile(s state.Store, prefix string, removalsOnly bool) *removedMeanwhile {
	m := &removedMeanwhile{Store: s, prefix: prefix, removalsOnly: removalsOnly}
	m.armed.Store(true)
	return m
}

func (s *removedMeanwhile) Update(ctx context.Context, fn func(ctx context.Context, tx state.Tx) error) error {
	return s.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		return fn(ctx, removedMeanwhileTx{Tx: tx, store: s})
	})
}

type removedMeanwhileTx struct {
	state.Tx
	store *removedMeanwhile
}

func (tx removedMeanwhileTx) races(namespace, key string) bool {
	return namespace == control.ReportsNamespace && strings.HasPrefix(key, tx.store.prefix) && tx.store.armed.CompareAndSwap(true, false)
}

func (tx removedMeanwhileTx) DeleteKV(ctx context.Context, namespace, key string, version int64) error {
	if tx.races(namespace, key) {
		if err := tx.Tx.DeleteKV(ctx, namespace, key, version); err != nil {
			return err
		}
		return core.NewError(core.CodeNotFound, "document "+key+" does not exist")
	}
	return tx.Tx.DeleteKV(ctx, namespace, key, version)
}

func (tx removedMeanwhileTx) PutKV(ctx context.Context, e state.KVEntry) (state.KVEntry, error) {
	if !tx.store.removalsOnly && tx.races(e.Namespace, e.Key) {
		return state.KVEntry{}, core.NewError(core.CodeConflict, "document "+e.Key+" is not at the version read")
	}
	return tx.Tx.PutKV(ctx, e)
}

// A report another publisher removed while this one published it does not
// fail the call: the report is gone as surely as if the read had missed it,
// so its issue is still recorded as published.
func TestAReportRemovedMeanwhileIsStillRecordedAsPublished(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		sendReportText(t, e, "Crash on save", "Saving twice crashes the app.", 1)
		store := meanwhile(e.store, "r/", true)
		p.Store = store
		res, err := p.Publish(context.Background())
		if err != nil {
			t.Fatalf("a report removed meanwhile fails the call: %v", err)
		}
		if store.armed.Load() {
			t.Fatal("the report was never removed; the test does not reach the race")
		}
		if res.Published != 1 || len(gh.created()) != 1 {
			t.Fatalf("a report removed meanwhile: %+v, %d issues", res, len(gh.created()))
		}
		if countKeys(t, e, "r/") != 0 || countKeys(t, e, "p/") != 1 {
			t.Fatalf("a report removed meanwhile: %d waiting, %d published", countKeys(t, e, "r/"), countKeys(t, e, "p/"))
		}
	})
}

// The refusal that gives up a report another publisher removed meanwhile
// writes nothing and does not fail the call: the other publisher may have opened the
// report's issue, so the charge this refusal would give back stays.
func TestARefusalOfAReportRemovedMeanwhileWritesNothing(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		sendReportText(t, e, "Odd report", "Something GitHub will not take.", 1)
		gh.fail = []error{rejected(http.StatusUnprocessableEntity), rejected(http.StatusUnprocessableEntity), rejected(http.StatusUnprocessableEntity)}
		// The last refusal gives the report up and removes it.
		for range 2 {
			publish(t, p)
		}
		store := meanwhile(e.store, "r/", true)
		p.Store = store
		if _, err := p.Publish(context.Background()); err != nil {
			t.Fatalf("a refusal of a report removed meanwhile fails the call: %v", err)
		}
		if store.armed.Load() {
			t.Fatal("the report was never removed; the test does not reach the race")
		}
		// The removal this test made rolled back with the refusal, so the
		// report waits as stored and nothing records it as given up.
		if countKeys(t, e, "r/") != 1 || countKeys(t, e, "f/") != 0 {
			t.Fatalf("a refusal of a report removed meanwhile: %d waiting, %d given up", countKeys(t, e, "r/"), countKeys(t, e, "f/"))
		}
	})
}

// The note of the last publishing error is shared by every publisher. One
// that clears it after another publisher did so meanwhile does not fail
// its call: the note left is the other publisher's, as recent.
func TestANoteOfThePublishingErrorChangedMeanwhileFailsNothing(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		sendReportText(t, e, "Search is slow", "It takes a minute.", 1)
		// The outage noted, a publication that clears the note after
		// another publisher did so meanwhile succeeds.
		gh.fail = []error{core.NewError(core.CodeUnavailable, "GitHub is down")}
		if _, err := p.Publish(context.Background()); err == nil {
			t.Fatal("a failed publication reported no error")
		}
		db.Clock.Advance(10 * time.Minute)
		cleared := meanwhile(e.store, "publish-error", false)
		p.Store = cleared
		res, err := p.Publish(context.Background())
		if err != nil || res.Published != 1 {
			t.Fatalf("a publication whose clearing of the note lost a race: %+v, %v", res, err)
		}
		if cleared.armed.Load() {
			t.Fatal("the note was never cleared; the test does not reach the race")
		}
	})
}

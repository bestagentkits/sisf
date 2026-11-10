package sqlstore_test

import (
	"errors"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// accept stores one delivery in a transaction of its own.
func accept(s *sqlstore.Store, e state.InboxEntry) (bool, error) {
	var accepted bool
	err := update(s, func(tx state.Tx) (err error) {
		accepted, err = tx.AcceptDelivery(ctx, e)
		return err
	})
	return accepted, err
}

// pendingDeliveries lists the delivery IDs still to be processed, in order.
func pendingDeliveries(t *testing.T, s *sqlstore.Store, limit int) []string {
	t.Helper()
	var ids []string
	if err := view(s, func(tx state.Tx) error {
		entries, err := tx.PendingDeliveries(ctx, limit)
		for _, e := range entries {
			ids = append(ids, e.DeliveryID)
		}
		return err
	}); err != nil {
		t.Fatalf("list pending deliveries: %v", err)
	}
	return ids
}

func TestDeliveryIsAcceptedOnceHoweverOftenItArrives(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		first := state.InboxEntry{Source: "github", DeliveryID: "d-1", EventType: "issues", Payload: []byte(`{"action":"labeled"}`)}
		if ok, err := accept(s, first); err != nil || !ok {
			t.Fatalf("first delivery = %v, %v", ok, err)
		}
		// The same delivery again, even with other content, is not stored.
		replay := first
		replay.Payload = []byte(`{"action":"forged"}`)
		for range 2 {
			if ok, err := accept(s, replay); err != nil || ok {
				t.Fatalf("a repeated delivery must be reported as seen, got %v, %v", ok, err)
			}
		}
		// The same ID from another source is a different delivery.
		if ok, err := accept(s, state.InboxEntry{Source: "poller", DeliveryID: "d-1"}); err != nil || !ok {
			t.Fatalf("delivery from another source = %v, %v", ok, err)
		}

		var stored []state.InboxEntry
		if err := view(s, func(tx state.Tx) (err error) {
			stored, err = tx.PendingDeliveries(ctx, 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(stored) != 2 || string(stored[0].Payload) != `{"action":"labeled"}` || stored[0].EventType != "issues" {
			t.Fatalf("stored deliveries = %+v", stored)
		}
		if !stored[0].ReceivedAt.Equal(db.Clock.Now()) || !stored[0].ProcessedAt.IsZero() {
			t.Fatalf("delivery timestamps = %s, %s", stored[0].ReceivedAt, stored[0].ProcessedAt)
		}

		for name, e := range map[string]state.InboxEntry{
			"no source":      {DeliveryID: "d-2"},
			"no delivery ID": {Source: "github"},
		} {
			if _, err := accept(s, e); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
			}
		}
	})
}

func TestPendingDeliveriesAreOldestFirstUntilProcessed(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		// Deliveries reach the store out of the order they were received in.
		late := db.Clock.Now().Add(time.Minute)
		for _, e := range []state.InboxEntry{
			{Source: "github", DeliveryID: "third", ReceivedAt: late},
			{Source: "github", DeliveryID: "first", ReceivedAt: db.Clock.Now()},
			{Source: "github", DeliveryID: "second", ReceivedAt: db.Clock.Now()},
		} {
			if ok, err := accept(s, e); err != nil || !ok {
				t.Fatalf("accept %s = %v, %v", e.DeliveryID, ok, err)
			}
		}
		if got := pendingDeliveries(t, s, 0); len(got) != 3 || got[0] != "first" || got[1] != "second" || got[2] != "third" {
			t.Fatalf("pending = %v", got)
		}
		if got := pendingDeliveries(t, s, 2); len(got) != 2 {
			t.Fatalf("pending with a limit of 2 = %v", got)
		}

		mustUpdate(t, s, func(tx state.Tx) error { return tx.MarkDeliveryProcessed(ctx, "github", "first") })
		// Marking again is harmless; an unknown delivery is an error.
		mustUpdate(t, s, func(tx state.Tx) error { return tx.MarkDeliveryProcessed(ctx, "github", "first") })
		wantCode(t, update(s, func(tx state.Tx) error { return tx.MarkDeliveryProcessed(ctx, "github", "unknown") }), core.CodeNotFound)
		if got := pendingDeliveries(t, s, 0); len(got) != 2 || got[0] != "second" {
			t.Fatalf("pending after processing = %v", got)
		}
		// A processed delivery stays known, so a redelivery is still a duplicate.
		if ok, err := accept(s, state.InboxEntry{Source: "github", DeliveryID: "first"}); err != nil || ok {
			t.Fatalf("redelivery of a processed delivery = %v, %v", ok, err)
		}
	})
}

// pendingOutbox lists the messages that are due, in order.
func pendingOutbox(t *testing.T, s *sqlstore.Store, limit int) []state.OutboxMessage {
	t.Helper()
	var messages []state.OutboxMessage
	if err := view(s, func(tx state.Tx) (err error) {
		messages, err = tx.PendingOutbox(ctx, limit)
		return err
	}); err != nil {
		t.Fatalf("list pending outbox: %v", err)
	}
	return messages
}

func TestOutboxKeepsOneMessagePerKeyAndRetriesWhenDue(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		comment := state.OutboxMessage{Topic: "github.comment", Key: "acme/app#7:progress", Payload: []byte("v1")}
		label := state.OutboxMessage{Topic: "github.label", Key: "acme/app#7:running"}
		mustUpdate(t, s, func(tx state.Tx) error {
			if err := tx.Enqueue(ctx, comment); err != nil {
				return err
			}
			// The same key again is a no-op, whatever it carries.
			comment.Payload = []byte("v2")
			if err := tx.Enqueue(ctx, comment); err != nil {
				return err
			}
			return tx.Enqueue(ctx, label)
		})
		pending := pendingOutbox(t, s, 0)
		if len(pending) != 2 || pending[0].Key != comment.Key || string(pending[0].Payload) != "v1" || pending[1].Key != label.Key {
			t.Fatalf("pending = %+v", pending)
		}
		if pending[0].ID == 0 || pending[0].ID >= pending[1].ID || !pending[0].CreatedAt.Equal(db.Clock.Now()) {
			t.Fatalf("message IDs and creation time = %+v", pending)
		}

		// A failed attempt is kept, counted and held back until its retry time.
		retryAt := db.Clock.Now().Add(time.Minute)
		mustUpdate(t, s, func(tx state.Tx) error { return tx.MarkOutboxFailed(ctx, pending[0].ID, retryAt, "rate limited") })
		if due := pendingOutbox(t, s, 0); len(due) != 1 || due[0].Key != label.Key {
			t.Fatalf("due before the retry time = %+v", due)
		}
		db.Clock.Advance(time.Minute)
		due := pendingOutbox(t, s, 1)
		if len(due) != 1 || due[0].Key != comment.Key || due[0].Attempts != 1 || due[0].LastError != "rate limited" {
			t.Fatalf("due at the retry time = %+v", due)
		}

		mustUpdate(t, s, func(tx state.Tx) error { return tx.MarkOutboxSent(ctx, pending[0].ID) })
		mustUpdate(t, s, func(tx state.Tx) error { return tx.MarkOutboxSent(ctx, pending[0].ID) })
		// A sent key stays taken, so the effect is not performed twice.
		mustUpdate(t, s, func(tx state.Tx) error { return tx.Enqueue(ctx, comment) })
		if due := pendingOutbox(t, s, 0); len(due) != 1 || due[0].Key != label.Key {
			t.Fatalf("due after sending = %+v", due)
		}

		wantCode(t, update(s, func(tx state.Tx) error { return tx.MarkOutboxSent(ctx, 999) }), core.CodeNotFound)
		wantCode(t, update(s, func(tx state.Tx) error { return tx.MarkOutboxFailed(ctx, 999, retryAt, "x") }), core.CodeNotFound)
		wantCode(t, update(s, func(tx state.Tx) error { return tx.MarkOutboxFailed(ctx, pending[0].ID, retryAt, "x") }), core.CodeConflict)
		wantCode(t, update(s, func(tx state.Tx) error { return tx.Enqueue(ctx, state.OutboxMessage{Topic: "github.comment"}) }), core.CodeInvalidArgument)
		wantCode(t, update(s, func(tx state.Tx) error { return tx.Enqueue(ctx, state.OutboxMessage{Key: "k"}) }), core.CodeInvalidArgument)
	})
}

func TestFailedTransactionLeavesNoDecisionAndNoSideEffect(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		failure := errors.New("the step after the write failed")
		err := update(s, func(tx state.Tx) error {
			if _, err := tx.Transition(ctx, received(issue(7))); err != nil {
				return err
			}
			if err := tx.Enqueue(ctx, state.OutboxMessage{Topic: "github.comment", Key: "acme/app#7:welcome"}); err != nil {
				return err
			}
			if _, err := tx.AcceptDelivery(ctx, state.InboxEntry{Source: "github", DeliveryID: "d-1"}); err != nil {
				return err
			}
			return failure
		})
		if !errors.Is(err, failure) {
			t.Fatalf("the caller's own error must come back unchanged, got %v", err)
		}
		wantCode(t, view(s, func(tx state.Tx) error {
			_, err := tx.GetRecord(ctx, issue(7))
			return err
		}), core.CodeNotFound)
		if len(pendingOutbox(t, s, 0)) != 0 || len(pendingDeliveries(t, s, 0)) != 0 {
			t.Fatal("a rolled back transaction left a side effect or a delivery behind")
		}
	})
}

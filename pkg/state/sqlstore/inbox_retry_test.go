package sqlstore_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

func fixedWait(d time.Duration) func(int) time.Duration { return func(int) time.Duration { return d } }

// Deliveries held back after a failure are left out of the pending list
// until their time, so a delivery behind a full batch of them is listed.
func TestDeferredDeliveriesDoNotFillTheBatch(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		for i := range 150 {
			if _, err := accept(s, state.InboxEntry{Source: "github", DeliveryID: fmt.Sprintf("poisoned-%03d", i)}); err != nil {
				t.Fatal(err)
			}
		}
		db.Clock.Advance(time.Second)
		if _, err := accept(s, state.InboxEntry{Source: "github", DeliveryID: "good"}); err != nil {
			t.Fatal(err)
		}
		for _, id := range pendingDeliveries(t, s, 100) {
			if _, err := s.DeferDelivery(ctx, "github", id, "UNAVAILABLE", fixedWait(time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		next := pendingDeliveries(t, s, 100)
		if len(next) != 51 || next[50] != "good" {
			t.Fatalf("the second batch = %d deliveries ending in %q, want the 50 untried and the good one", len(next), next[len(next)-1])
		}

		// Once their wait is over the deferred ones are due again, oldest first.
		db.Clock.Advance(time.Minute)
		if again := pendingDeliveries(t, s, 1); len(again) != 1 || again[0] != "poisoned-000" {
			t.Fatalf("the deferred deliveries did not come back: %v", again)
		}
	})
}

// Each failure is counted, its reason kept, and the wait grows with the
// count the caller is told.
func TestADeferredDeliveryCountsItsFailures(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		if _, err := accept(s, state.InboxEntry{Source: "github", DeliveryID: "d-1"}); err != nil {
			t.Fatal(err)
		}
		wait := func(failures int) time.Duration { return time.Duration(failures) * time.Minute }
		for want := 1; want <= 3; want++ {
			got, err := s.DeferDelivery(ctx, "github", "d-1", fmt.Sprintf("UNAVAILABLE: try %d", want), wait)
			if err != nil || got != want {
				t.Fatalf("failure %d counted as %d, %v", want, got, err)
			}
		}
		record, err := s.Delivery(ctx, "github", "d-1")
		if err != nil {
			t.Fatal(err)
		}
		if record.Attempts != 3 || record.LastError != "UNAVAILABLE: try 3" || record.Outcome != "" ||
			!record.NextAttemptAt.Equal(db.Clock.Now().Add(3*time.Minute)) {
			t.Fatalf("record = %+v", record)
		}
	})
}

// A delivery that leaves the inbox for good keeps how and why, is not
// listed again, and cannot be deferred any more.
func TestAFinishedDeliveryKeepsItsOutcome(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		for _, id := range []string{"gone", "parked", "handled"} {
			if _, err := accept(s, state.InboxEntry{Source: "github", DeliveryID: id}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.FinishDelivery(ctx, "github", "gone", sqlstore.DeliveryGone, "NOT_FOUND: GitHub answered 404"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DeferDelivery(ctx, "github", "parked", "UNAVAILABLE: GitHub could not be reached", fixedWait(0)); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishDelivery(ctx, "github", "parked", sqlstore.DeliveryParked, ""); err != nil {
			t.Fatal(err)
		}
		mustUpdate(t, s, func(tx state.Tx) error { return tx.MarkDeliveryProcessed(ctx, "github", "handled") })
		if left := pendingDeliveries(t, s, 0); len(left) != 0 {
			t.Fatalf("finished deliveries are still pending: %v", left)
		}
		for id, want := range map[string]struct {
			outcome sqlstore.DeliveryOutcome
			reason  string
		}{
			"gone":    {sqlstore.DeliveryGone, "NOT_FOUND: GitHub answered 404"},
			"parked":  {sqlstore.DeliveryParked, "UNAVAILABLE: GitHub could not be reached"},
			"handled": {sqlstore.DeliveryHandled, ""},
		} {
			record, err := s.Delivery(ctx, "github", id)
			if err != nil || record.Outcome != want.outcome || record.LastError != want.reason || record.ProcessedAt.IsZero() {
				t.Fatalf("%s: %+v, %v", id, record, err)
			}
		}

		// The first outcome stands.
		if err := s.FinishDelivery(ctx, "github", "gone", sqlstore.DeliveryHandled, "later"); err != nil {
			t.Fatal(err)
		}
		if record, _ := s.Delivery(ctx, "github", "gone"); record.Outcome != sqlstore.DeliveryGone {
			t.Fatalf("a second finish overwrote the outcome: %+v", record)
		}
		_, err := s.DeferDelivery(ctx, "github", "gone", "late failure", fixedWait(time.Minute))
		wantCode(t, err, core.CodeConflict)
		_, err = s.DeferDelivery(ctx, "github", "never-accepted", "x", fixedWait(time.Minute))
		wantCode(t, err, core.CodeNotFound)
		wantCode(t, s.FinishDelivery(ctx, "github", "never-accepted", sqlstore.DeliveryGone, ""), core.CodeNotFound)
		wantCode(t, s.FinishDelivery(ctx, "github", "handled", "lost", ""), core.CodeInvalidArgument)
	})
}

// Only the control zone keeps the inbox.
func TestTheAgentZoneCannotDeferOrFinishADelivery(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		if _, err := accept(control, state.InboxEntry{Source: "github", DeliveryID: "d-1"}); err != nil {
			t.Fatal(err)
		}
		agent := db.Open(t, core.ZoneAgent)
		_, err := agent.DeferDelivery(ctx, "github", "d-1", "x", fixedWait(time.Minute))
		wantCode(t, err, core.CodePermissionDenied)
		wantCode(t, agent.FinishDelivery(ctx, "github", "d-1", sqlstore.DeliveryGone, ""), core.CodePermissionDenied)
		_, err = agent.Delivery(ctx, "github", "d-1")
		wantCode(t, err, core.CodePermissionDenied)
	})
}

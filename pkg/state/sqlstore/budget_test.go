package sqlstore_test

import (
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// reservation is money set aside for the first run of an issue.
func reservation(id string, amount core.Micros) core.Reservation {
	return core.Reservation{
		ID: id, IssueID: issue(7), RunID: "run-1", Mode: core.BudgetCapped,
		Amount: amount, Status: core.ReservationHeld, HardCapEnforced: true, IdempotencyKey: "claim-" + id,
	}
}

// spend is one metered cost of the first run of an issue.
func spend(cost core.Micros, verified bool, at time.Time) core.Usage {
	return core.Usage{
		IssueID: issue(7), RunID: "run-1", ReservationID: "r-1", Step: "coding",
		ModelRoute:  core.ModelRoute{Provider: "fake", Model: "scripted"},
		InputTokens: 10, OutputTokens: 20, Cost: cost, Verified: verified, At: at,
	}
}

// totals reads the ledger summary since an instant.
func totals(t *testing.T, s *sqlstore.Store, since time.Time) core.BudgetTotals {
	t.Helper()
	var sum core.BudgetTotals
	if err := view(s, func(tx state.Tx) (err error) {
		sum, err = tx.BudgetTotals(ctx, since)
		return err
	}); err != nil {
		t.Fatalf("budget totals: %v", err)
	}
	return sum
}

func TestReservationIsStoredAndUpdatedByID(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneAgent)
		held := reservation("r-1", 5_000_000)
		mustUpdate(t, s, func(tx state.Tx) error { return tx.PutReservation(ctx, held) })

		var stored core.Reservation
		get := func(tx state.Tx) (err error) {
			stored, err = tx.GetReservation(ctx, "r-1")
			return err
		}
		if err := view(s, get); err != nil {
			t.Fatal(err)
		}
		held.FactoryID, held.CreatedAt = sqlstoretest.Factory, db.Clock.Now()
		if stored != held {
			t.Fatalf("stored reservation = %+v, want %+v", stored, held)
		}

		// Settling changes the status and amount of the same reservation.
		db.Clock.Advance(time.Minute)
		settled := held
		settled.Status, settled.Amount = core.ReservationSettled, 4_200_000
		mustUpdate(t, s, func(tx state.Tx) error { return tx.PutReservation(ctx, settled) })
		if err := view(s, get); err != nil {
			t.Fatal(err)
		}
		if stored != settled {
			t.Fatalf("settled reservation = %+v, want %+v", stored, settled)
		}

		wantCode(t, view(s, func(tx state.Tx) error {
			_, err := tx.GetReservation(ctx, "unknown")
			return err
		}), core.CodeNotFound)
	})
}

func TestReservationCannotBeMalformedOrMovedToAnotherRun(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneAgent)
		mustUpdate(t, s, func(tx state.Tx) error { return tx.PutReservation(ctx, reservation("r-1", 5_000_000)) })

		put := func(change func(*core.Reservation)) error {
			r := reservation("r-1", 5_000_000)
			change(&r)
			return update(s, func(tx state.Tx) error { return tx.PutReservation(ctx, r) })
		}
		// Money set aside for one run cannot be handed to another.
		wantCode(t, put(func(r *core.Reservation) { r.RunID = "run-2" }), core.CodeConflict)
		wantCode(t, put(func(r *core.Reservation) { r.IssueID = issue(8) }), core.CodeConflict)
		wantCode(t, put(func(r *core.Reservation) { r.IdempotencyKey = "another-claim" }), core.CodeConflict)

		for name, change := range map[string]func(*core.Reservation){
			"no ID":           func(r *core.Reservation) { r.ID = "" },
			"negative amount": func(r *core.Reservation) { r.Amount = -1 },
			"unknown status":  func(r *core.Reservation) { r.Status = "spent" },
			"no budget mode":  func(r *core.Reservation) { r.Mode = "" },
			"no run":          func(r *core.Reservation) { r.RunID = "" },
			"no issue":        func(r *core.Reservation) { r.IssueID = core.IssueRef{} },
			"no key":          func(r *core.Reservation) { r.IdempotencyKey = "" },
			"other factory":   func(r *core.Reservation) { r.FactoryID = "another-factory" },
		} {
			if err := put(change); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
			}
		}
		if got := totals(t, s, time.Time{}); got.Reserved != 5_000_000 {
			t.Fatalf("refused writes changed the reserved amount: %+v", got)
		}
	})
}

func TestBudgetTotalsCountHeldReservationsAndUsageSinceTheInstant(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneAgent)
		if got := totals(t, s, time.Time{}); got != (core.BudgetTotals{}) {
			t.Fatalf("totals of an empty ledger = %+v", got)
		}
		periodStart := db.Clock.Now()
		released := reservation("r-3", 9_000_000)
		released.Status = core.ReservationReleased
		mustUpdate(t, s, func(tx state.Tx) error {
			for _, r := range []core.Reservation{reservation("r-1", 5_000_000), reservation("r-2", 1_000_000), released} {
				if err := tx.PutReservation(ctx, r); err != nil {
					return err
				}
			}
			for _, u := range []core.Usage{
				spend(700, true, periodStart.Add(-time.Hour)), // before the period
				spend(300, true, periodStart),
				spend(200, true, periodStart.Add(time.Minute)),
				spend(50, false, periodStart.Add(time.Minute)),
				spend(40, false, time.Time{}), // stamped with the instant of the write
			} {
				if err := tx.AppendUsage(ctx, u); err != nil {
					return err
				}
			}
			return nil
		})
		want := core.BudgetTotals{Reserved: 6_000_000, SpentVerified: 500, SpentUnverified: 90}
		if got := totals(t, s, periodStart); got != want {
			t.Fatalf("totals since the period start = %+v, want %+v", got, want)
		}
		if got := totals(t, s, time.Time{}); got.SpentVerified != 1200 {
			t.Fatalf("totals since the beginning = %+v", got)
		}
	})
}

func TestUsageThatWouldLowerSpendingIsRefused(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneAgent)
		for name, change := range map[string]func(*core.Usage){
			"negative cost":   func(u *core.Usage) { u.Cost = -1 },
			"negative tokens": func(u *core.Usage) { u.OutputTokens = -5 },
			"malformed run":   func(u *core.Usage) { u.RunID = "Run 1" },
			"malformed issue": func(u *core.Usage) { u.IssueID = core.IssueRef{Owner: "acme"} },
			"other factory":   func(u *core.Usage) { u.FactoryID = "another-factory" },
		} {
			u := spend(100, true, db.Clock.Now())
			change(&u)
			err := update(s, func(tx state.Tx) error { return tx.AppendUsage(ctx, u) })
			if !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
			}
		}
		if got := totals(t, s, time.Time{}); got != (core.BudgetTotals{}) {
			t.Fatalf("refused usage reached the ledger: %+v", got)
		}
	})
}

func TestRecordedUsageCannotBeEditedOrDeleted(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneAgent)
		mustUpdate(t, s, func(tx state.Tx) error { return tx.AppendUsage(ctx, spend(900, true, db.Clock.Now())) })
		raw := db.Raw(t)
		if _, err := raw.ExecContext(ctx, `UPDATE usage_ledger SET cost = 0`); err == nil {
			t.Error("the database let recorded usage be edited")
		}
		if _, err := raw.ExecContext(ctx, `DELETE FROM usage_ledger`); err == nil {
			t.Error("the database let recorded usage be deleted")
		}
		if got := totals(t, s, time.Time{}); got.SpentVerified != 900 {
			t.Fatalf("usage after the refused changes = %+v", got)
		}
	})
}

func TestReservationRollsBackWithTheClaimItPaysFor(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneAgent)
		ready := readyRecord(t, db.Open(t, core.ZoneControl), issue(7))
		// The claim loses on a stale version after the money was set aside.
		err := update(s, func(tx state.Tx) error {
			if err := tx.PutReservation(ctx, reservation("r-1", 5_000_000)); err != nil {
				return err
			}
			_, err := tx.Transition(ctx, step(issue(7), core.StateReady, core.EventClaimed, core.StateClaimed, ready.Version-1, "claim"))
			return err
		})
		wantCode(t, err, core.CodeConflict)
		if got := totals(t, s, time.Time{}); got.Reserved != 0 {
			t.Fatalf("a failed claim kept its reservation: %+v", got)
		}
		err = view(s, func(tx state.Tx) error {
			_, err := tx.GetReservation(ctx, "r-1")
			return err
		})
		wantCode(t, err, core.CodeNotFound)
	})
}

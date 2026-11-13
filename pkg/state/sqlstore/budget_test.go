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
		s := db.Open(t, core.ZoneControl)
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

		// Settling changes the status of the same reservation.
		db.Clock.Advance(time.Minute)
		settled := held
		settled.Status = core.ReservationSettled
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
		s := db.Open(t, core.ZoneControl)
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

// A reservation is made held, keeps the terms it was made on, and its status
// only moves on: settled and released are final, and a suspended run's money
// is never held again. So no zone can free money a run set aside by
// rewriting the reservation, nor bring back a reservation already closed.
func TestReservationKeepsItsTermsAndItsStatusOnlyMovesOn(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		put := func(r core.Reservation) error {
			return update(s, func(tx state.Tx) error { return tx.PutReservation(ctx, r) })
		}
		with := func(id string, change func(*core.Reservation)) core.Reservation {
			r := reservation(id, 5_000_000)
			change(&r)
			return r
		}
		for _, status := range []core.ReservationStatus{core.ReservationSettled, core.ReservationReleased, core.ReservationSuspended} {
			wantCode(t, put(with("r-new-"+string(status), func(r *core.Reservation) { r.Status = status })), core.CodeInvalidArgument)
		}

		if err := put(reservation("r-1", 5_000_000)); err != nil {
			t.Fatal(err)
		}
		for name, change := range map[string]func(*core.Reservation){
			"a lower amount":         func(r *core.Reservation) { r.Amount = 0 },
			"another budget mode":    func(r *core.Reservation) { r.Mode = core.BudgetUnlimited },
			"no provider hard cap":   func(r *core.Reservation) { r.HardCapEnforced = false },
			"a lower amount, closed": func(r *core.Reservation) { r.Amount, r.Status = 1, core.ReservationSettled },
		} {
			if err := put(with("r-1", change)); !core.HasCode(err, core.CodeConflict) {
				t.Errorf("a reservation rewritten with %s: want %s, got %v", name, core.CodeConflict, err)
			}
		}
		if got := totals(t, s, time.Time{}); got.Reserved != 5_000_000 {
			t.Fatalf("refused rewrites changed the reserved amount: %+v", got)
		}

		status := func(id string, to core.ReservationStatus) error {
			return put(with(id, func(r *core.Reservation) { r.Status = to }))
		}
		for _, step := range []struct {
			id   string
			to   core.ReservationStatus
			want core.ErrorCode
		}{
			{"r-1", core.ReservationSuspended, ""},
			{"r-1", core.ReservationSuspended, ""},
			{"r-1", core.ReservationHeld, core.CodeConflict},
			{"r-1", core.ReservationReleased, ""},
			{"r-1", core.ReservationReleased, ""},
			{"r-1", core.ReservationHeld, core.CodeConflict},
			{"r-1", core.ReservationSettled, core.CodeConflict},
			{"r-1", core.ReservationSuspended, core.CodeConflict},
			{"r-2", core.ReservationHeld, ""},
			{"r-2", core.ReservationSettled, ""},
			{"r-2", core.ReservationHeld, core.CodeConflict},
			{"r-2", core.ReservationReleased, core.CodeConflict},
			{"r-2", core.ReservationSuspended, core.CodeConflict},
		} {
			err := status(step.id, step.to)
			if step.want == "" && err != nil {
				t.Errorf("%s to %s must be allowed: %v", step.id, step.to, err)
			}
			if step.want != "" && !core.HasCode(err, step.want) {
				t.Errorf("%s to %s: want %s, got %v", step.id, step.to, step.want, err)
			}
		}
	})
}

// The agent zone makes the reservation of its claim and changes none:
// closing a run's reservation is the control plane's and the supervisor's,
// so a stolen agent credential cannot free money a live run set aside.
func TestAgentZoneMakesAReservationAndChangesNone(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		agent := db.Open(t, core.ZoneAgent)
		mustUpdate(t, agent, func(tx state.Tx) error { return tx.PutReservation(ctx, reservation("r-1", 5_000_000)) })
		for _, status := range []core.ReservationStatus{core.ReservationSettled, core.ReservationSuspended, core.ReservationReleased} {
			r := reservation("r-1", 5_000_000)
			r.Status = status
			wantCode(t, update(agent, func(tx state.Tx) error { return tx.PutReservation(ctx, r) }), core.CodePermissionDenied)
		}
		if got := totals(t, agent, time.Time{}); got.Reserved != 5_000_000 {
			t.Fatalf("the agent zone changed its reservation: %+v", got)
		}
	})
}

func TestBudgetTotalsCountHeldReservationsAndUsageSinceTheInstant(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		if got := totals(t, s, time.Time{}); got != (core.BudgetTotals{}) {
			t.Fatalf("totals of an empty ledger = %+v", got)
		}
		periodStart := db.Clock.Now()
		released := reservation("r-3", 9_000_000)
		released.Status = core.ReservationReleased
		mustUpdate(t, s, func(tx state.Tx) error {
			// r-3 is made held and then released, as a run that gives it up does.
			for _, r := range []core.Reservation{reservation("r-1", 5_000_000), reservation("r-2", 1_000_000), reservation("r-3", 9_000_000), released} {
				if err := tx.PutReservation(ctx, r); err != nil {
					return err
				}
			}
			for _, u := range []core.Usage{
				spend(50, false, periodStart.Add(time.Minute)),
				spend(40, false, time.Time{}), // stamped with the instant of the write
			} {
				if err := tx.AppendUsage(ctx, u); err != nil {
					return err
				}
			}
			return nil
		})
		// No zone books verified usage; rows a past writer left are still
		// counted apart.
		rawUsage(t, db, "run-1", 700, true, periodStart.Add(-time.Hour)) // before the period
		rawUsage(t, db, "run-1", 300, true, periodStart)
		rawUsage(t, db, "run-1", 200, true, periodStart.Add(time.Minute))
		want := core.BudgetTotals{Reserved: 6_000_000, SpentVerified: 500, SpentUnverified: 90}
		if got := totals(t, s, periodStart); got != want {
			t.Fatalf("totals since the period start = %+v, want %+v", got, want)
		}
		if got := totals(t, s, time.Time{}); got.SpentVerified != 1200 {
			t.Fatalf("totals since the beginning = %+v", got)
		}
	})
}

// Unverified usage with tokens and no cost is counted apart, whatever its
// run reserved. Usage with a cost, verified usage and usage without tokens
// are not.
func TestBudgetTotalsCountUsageWithoutACost(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		periodStart := db.Clock.Now()
		nothingReserved := reservation("r-3", 0)
		nothingReserved.RunID = "run-3"
		forRun := func(run core.RunID, u core.Usage) core.Usage {
			u.RunID, u.ReservationID = run, ""
			return u
		}
		noTokens := spend(0, false, periodStart)
		noTokens.InputTokens, noTokens.OutputTokens = 0, 0
		mustUpdate(t, s, func(tx state.Tx) error {
			for _, r := range []core.Reservation{reservation("r-1", 5_000_000), nothingReserved} {
				if err := tx.PutReservation(ctx, r); err != nil {
					return err
				}
			}
			for _, u := range []core.Usage{
				spend(0, false, periodStart.Add(-time.Hour)),  // before the period
				spend(0, false, periodStart),                  // its run reserved 5,000,000
				forRun("run-2", spend(0, false, periodStart)), // its run reserved nothing
				forRun("run-3", spend(0, false, periodStart)), // its run reserved 0
				spend(60, false, periodStart),                 // priced
				noTokens,                                      // no cost stated, no tokens: still unpriced
			} {
				if err := tx.AppendUsage(ctx, u); err != nil {
					return err
				}
			}
			return nil
		})
		rawUsage(t, db, "run-1", 0, true, periodStart) // verified
		want := core.BudgetTotals{Reserved: 5_000_000, SpentUnverified: 60, Unpriced: 4}
		if got := totals(t, s, periodStart); got != want {
			t.Fatalf("totals since the period start = %+v, want %+v", got, want)
		}
	})
}

// Token counts whose sum would not fit in a BIGINT are counted like any
// other, and reading the totals does not fail on them.
func TestBudgetTotalsCountUsageWithHugeTokenCounts(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		huge := spend(0, false, db.Clock.Now())
		huge.InputTokens, huge.OutputTokens = 1<<62, 1<<62
		priced := huge
		priced.Cost = 7
		mustUpdate(t, s, func(tx state.Tx) error {
			for _, u := range []core.Usage{huge, priced} {
				if err := tx.AppendUsage(ctx, u); err != nil {
					return err
				}
			}
			return nil
		})
		if got, want := totals(t, s, time.Time{}), (core.BudgetTotals{SpentUnverified: 7, Unpriced: 1}); got != want {
			t.Fatalf("totals = %+v, want %+v", got, want)
		}
	})
}

func TestUsageThatWouldLowerSpendingIsRefused(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		for name, change := range map[string]func(*core.Usage){
			"negative cost":   func(u *core.Usage) { u.Cost = -1 },
			"negative tokens": func(u *core.Usage) { u.OutputTokens = -5 },
			"malformed run":   func(u *core.Usage) { u.RunID = "Run 1" },
			"malformed issue": func(u *core.Usage) { u.IssueID = core.IssueRef{Owner: "acme"} },
			"other factory":   func(u *core.Usage) { u.FactoryID = "another-factory" },
		} {
			u := spend(100, false, db.Clock.Now())
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
		s := db.Open(t, core.ZoneControl)
		mustUpdate(t, s, func(tx state.Tx) error { return tx.AppendUsage(ctx, spend(900, false, db.Clock.Now())) })
		raw := db.Raw(t)
		if _, err := raw.ExecContext(ctx, `UPDATE usage_ledger SET cost = 0`); err == nil {
			t.Error("the database let recorded usage be edited")
		}
		if _, err := raw.ExecContext(ctx, `DELETE FROM usage_ledger`); err == nil {
			t.Error("the database let recorded usage be deleted")
		}
		if got := totals(t, s, time.Time{}); got.SpentUnverified != 900 {
			t.Fatalf("usage after the refused changes = %+v", got)
		}
	})
}

func TestReservationRollsBackWithTheClaimItPaysFor(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s, control := db.Open(t, core.ZoneAgent), db.Open(t, core.ZoneControl)
		ready := readyRecord(t, control, issue(7))
		// The claim loses on a stale version after the money was set aside.
		err := update(s, func(tx state.Tx) error {
			if err := tx.PutReservation(ctx, reservation("r-1", 5_000_000)); err != nil {
				return err
			}
			_, err := tx.Transition(ctx, step(issue(7), core.StateReady, core.EventClaimed, core.StateClaimed, ready.Version-1, "claim"))
			return err
		})
		wantCode(t, err, core.CodeConflict)
		if got := totals(t, control, time.Time{}); got.Reserved != 0 {
			t.Fatalf("a failed claim kept its reservation: %+v", got)
		}
		err = view(s, func(tx state.Tx) error {
			_, err := tx.GetReservation(ctx, "r-1")
			return err
		})
		wantCode(t, err, core.CodeNotFound)
	})
}

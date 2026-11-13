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

// rawUsage inserts a ledger row with plain SQL, as a writer holding a zone's
// database credential could, bypassing the store's own checks.
func rawUsage(t *testing.T, db *sqlstoretest.Database, run core.RunID, cost int64, verified bool, at time.Time) {
	t.Helper()
	statement := fmt.Sprintf(`INSERT INTO usage_ledger (factory_id, issue, run_id, reservation_id, step, model_route,
		input_tokens, output_tokens, cost, verified, recorded_at) VALUES ('%s', 'acme/app#7', '%s', '', 'coding', '{}', 1, 1, %d, %t, %d)`,
		sqlstoretest.Factory, run, cost, verified, at.UnixNano())
	if _, err := db.Raw(t).ExecContext(ctx, statement); err != nil {
		t.Fatal(err)
	}
}

// A row the agent zone books is stamped with the store's own clock, whatever
// instant the writer gave: a row dated long ago would otherwise lower a
// run's charge at its close while counting in no budget period.
func TestTheAgentZoneCannotBackdateUsage(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control, agent := db.Open(t, core.ZoneControl), db.Open(t, core.ZoneAgent)
		periodStart := db.Clock.Now()
		mustUpdate(t, control, func(tx state.Tx) error { return tx.PutReservation(ctx, reservation("r-1", 1_000_000)) })
		mustUpdate(t, agent, func(tx state.Tx) error {
			return tx.AppendUsage(ctx, spend(1_000_000, false, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)))
		})
		if got := totals(t, control, periodStart); got.SpentUnverified != 1_000_000 {
			t.Fatalf("the period's totals are %+v, want the backdated row counted now", got)
		}
	})
}

// Only rows recorded at or after the instant asked for count for a run: a
// row written into the ledger with an earlier date, by plain SQL, cannot
// lower the charge of a reservation made after it.
func TestRunUsageCountsOnlyRowsSinceTheInstant(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		reserved := db.Clock.Now()
		rawUsage(t, db, "run-1", 900_000, false, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
		rawUsage(t, db, "run-1", 100, false, reserved)
		for _, zone := range []core.Zone{core.ZoneControl, core.ZoneAgent} {
			var got core.RunUsage
			if err := view(db.Open(t, zone), func(tx state.Tx) (err error) {
				got, err = tx.RunUsage(ctx, "run-1", reserved)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if want := (core.RunUsage{Cost: 100, Entries: 1}); got != want {
				t.Errorf("%s zone: RunUsage = %+v, want %+v", zone, got, want)
			}
		}
	})
}

// No zone may book usage as verified: nothing in the factory confirms a
// cost with its provider, so such a row could only be a writer's claim.
func TestVerifiedUsageIsRefused(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		mustUpdate(t, control, func(tx state.Tx) error { return tx.PutReservation(ctx, reservation("r-1", 1_000_000)) })
		for _, zone := range []core.Zone{core.ZoneControl, core.ZoneAgent} {
			err := update(db.Open(t, zone), func(tx state.Tx) error { return tx.AppendUsage(ctx, spend(5, true, db.Clock.Now())) })
			if !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("%s zone booked verified usage: %v", zone, err)
			}
		}
		if got := totals(t, control, time.Time{}); got.SpentVerified != 0 || got.SpentUnverified != 0 {
			t.Fatalf("refused usage reached the ledger: %+v", got)
		}
	})
}

// The agent zone books usage only against a reservation of the run the row
// names: not without a run or a reservation, not against an unknown
// reservation and not against another run's.
func TestTheAgentZoneBooksUsageOnlyAgainstItsRunsReservation(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control, agent := db.Open(t, core.ZoneControl), db.Open(t, core.ZoneAgent)
		other := reservation("r-2", 1_000_000)
		other.RunID, other.IdempotencyKey = "run-2", "claim-r-2"
		mustUpdate(t, control, func(tx state.Tx) error {
			if err := tx.PutReservation(ctx, reservation("r-1", 1_000_000)); err != nil {
				return err
			}
			return tx.PutReservation(ctx, other)
		})
		for name, change := range map[string]func(*core.Usage){
			"no run":                func(u *core.Usage) { u.RunID = "" },
			"no reservation":        func(u *core.Usage) { u.ReservationID = "" },
			"unknown reservation":   func(u *core.Usage) { u.ReservationID = "r-9" },
			"another run's":         func(u *core.Usage) { u.ReservationID = "r-2" },
			"another issue's":       func(u *core.Usage) { u.IssueID = issue(8) },
			"another run, same res": func(u *core.Usage) { u.RunID = "run-2" },
		} {
			u := spend(5, false, db.Clock.Now())
			change(&u)
			if err := update(agent, func(tx state.Tx) error { return tx.AppendUsage(ctx, u) }); !core.HasCode(err, core.CodePermissionDenied) {
				t.Errorf("%s: want %s, got %v", name, core.CodePermissionDenied, err)
			}
		}
		mustUpdate(t, agent, func(tx state.Tx) error { return tx.AppendUsage(ctx, spend(5, false, db.Clock.Now())) })
		if got := totals(t, control, time.Time{}); got.SpentUnverified != 5 {
			t.Fatalf("the ledger shows %+v, want only the row against the run's own reservation", got)
		}
	})
}

// A cost above core.MaxUsageCost is booked at that bound, and sums that
// would not fit in a BIGINT, as rows written by plain SQL can make them,
// are read capped rather than failing the read.
func TestHugeCostsAreBoundedAndSummedSafely(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		since := db.Clock.Now()
		mustUpdate(t, control, func(tx state.Tx) error { return tx.AppendUsage(ctx, spend(core.MaxUsageCost*5, false, since)) })
		if got := totals(t, control, since); got.SpentUnverified != core.MaxUsageCost {
			t.Fatalf("a cost above the bound was booked as %d, want %d", got.SpentUnverified, core.MaxUsageCost)
		}
		rawUsage(t, db, "run-1", 5e18, false, since)
		rawUsage(t, db, "run-1", 5e18, false, since)
		got := totals(t, control, since)
		if got.SpentUnverified < core.MaxUsageCost || got.SpentUnverified <= 0 {
			t.Fatalf("totals over a BIGINT = %+v, want a large positive sum", got)
		}
		var run core.RunUsage
		if err := view(control, func(tx state.Tx) (err error) {
			run, err = tx.RunUsage(ctx, "run-1", since)
			return err
		}); err != nil {
			t.Fatalf("summing a run's usage over a BIGINT failed: %v", err)
		}
		if run.Cost <= 0 || run.Entries != 3 {
			t.Fatalf("RunUsage over a BIGINT = %+v, want a large positive sum of 3 entries", run)
		}
	})
}

// dropUsageChecks takes away the database's check that usage has no
// negative cost, standing for a ledger an earlier release left without it
// or a check that was never validated against existing rows.
func dropUsageChecks(t *testing.T, db *sqlstoretest.Database) {
	t.Helper()
	if db.Dialect != sqlstore.DialectPostgres {
		return
	}
	if _, err := db.Raw(t).ExecContext(ctx, `ALTER TABLE usage_ledger DROP CONSTRAINT IF EXISTS "sisf_usage_values"`); err != nil {
		t.Fatal(err)
	}
}

// A negative cost, which no store writes, counts for nothing in a sum and
// marks the run's usage as tampered: a row of -1e18 neither frees budget
// headroom nor lowers a run's charge, and sums of such rows never fail.
func TestNegativeCostsCountForNothingAndMarkTheRun(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		dropUsageChecks(t, db)
		control := db.Open(t, core.ZoneControl)
		since := db.Clock.Now()
		rawUsage(t, db, "run-1", 700, false, since)
		rawUsage(t, db, "run-1", -1e18, false, since)
		rawUsage(t, db, "run-1", -5e18, false, since)
		rawUsage(t, db, "run-1", -5e18, false, since)
		if got := totals(t, control, since); got.SpentUnverified != 700 {
			t.Fatalf("totals with negative rows = %+v, want 700 unverified", got)
		}
		var run core.RunUsage
		if err := view(control, func(tx state.Tx) (err error) {
			run, err = tx.RunUsage(ctx, "run-1", since)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if want := (core.RunUsage{Cost: 700, Entries: 4, CostTampered: true}); run != want {
			t.Fatalf("RunUsage = %+v, want %+v", run, want)
		}
	})
}

// A reservation the agent zone makes is dated by the store's own clock,
// whatever instant the writer gave: a reservation dated long ago would let
// rows dated before the budget period count for its run's close.
func TestTheAgentZoneCannotBackdateAReservation(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		agent := db.Open(t, core.ZoneAgent)
		r := reservation("r-1", 1_000_000)
		r.CreatedAt = time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
		mustUpdate(t, agent, func(tx state.Tx) error { return tx.PutReservation(ctx, r) })
		var got core.Reservation
		if err := view(agent, func(tx state.Tx) (err error) {
			got, err = tx.GetReservation(ctx, "r-1")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if !got.CreatedAt.Equal(db.Clock.Now()) {
			t.Fatalf("the agent zone's reservation is dated %s, want the store's clock %s", got.CreatedAt, db.Clock.Now())
		}
	})
}

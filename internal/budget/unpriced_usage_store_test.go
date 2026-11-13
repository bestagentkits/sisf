package budget_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// statusAfter confirms an unlimited budget with no alert on the real store,
// configures settings, runs book in the same transaction and reads what
// budget.get then shows.
func statusAfter(t *testing.T, db *sqlstoretest.Database, settings budget.Settings,
	book func(ctx context.Context, gate *budget.Gate, tx state.Tx) error) budget.Status {
	t.Helper()
	ctx := context.Background()
	store := db.Open(t, core.ZoneControl)
	gate := budget.NewGate(budget.Config{Clock: db.Clock})
	err := store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		for key, v := range map[string]any{
			dispatcher.ConfirmationKey: dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true,
				ConfirmedAt: db.Clock.Now().Add(-time.Hour)},
			budget.SettingsKey: settings,
		} {
			raw, err := json.Marshal(v)
			if err != nil {
				return err
			}
			if _, err := tx.PutKV(ctx, state.KVEntry{Namespace: budget.SettingsNamespace, Key: key, Value: raw}); err != nil {
				return err
			}
		}
		return book(ctx, gate, tx)
	})
	if err != nil {
		t.Fatal(err)
	}
	var st budget.Status
	if err := store.View(ctx, func(ctx context.Context, tx state.Tx) (err error) {
		st, err = budget.ReadStatus(ctx, dispatcher.KVConfirmation{}, tx, db.Clock.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return st
}

// bookRunWithoutACost reserves estimate for run 1, books an attempt whose
// agent reported tokens and no cost as the dispatcher books one, and closes
// the run's reservation as the factory closes it. The run's record claims
// nothing: the ledger alone shows that the usage stated no cost.
func bookRunWithoutACost(ctx context.Context, gate *budget.Gate, tx state.Tx, estimate core.Micros) error {
	req := request(1, estimate)
	req.FactoryID = sqlstoretest.Factory
	r, err := gate.Reserve(ctx, tx, req)
	if err != nil {
		return err
	}
	u := tokensOnly(1)
	u.FactoryID, u.ReservationID, u.Step = sqlstoretest.Factory, r.ID, core.StepCoding
	if err := tx.AppendUsage(ctx, u); err != nil {
		return err
	}
	return dispatcher.CloseReservationOfRun(ctx, gate, tx, r.RunID, r.ID, false)
}

// unpricedWarning is the one warning about usage without a reported cost.
// It may say what such usage counts through, never that it was charged:
// budget.get does not know whether anything was. It says that only the part
// of the reservation not covered by reported costs can be charged.
func unpricedWarning(t *testing.T, st budget.Status) {
	t.Helper()
	var found []string
	for _, w := range st.Warnings {
		if strings.Contains(w, "no cost reported") {
			found = append(found, w)
		}
	}
	if len(found) != 1 {
		t.Fatalf("warnings = %v, want one about usage with no cost reported", st.Warnings)
	}
	for _, claim := range []string{"was charged", "it is charged", "is charged when", "charged at", "is not counted in"} {
		if strings.Contains(found[0], claim) {
			t.Errorf("the warning %q claims %q, which budget.get cannot know", found[0], claim)
		}
	}
	if !strings.Contains(found[0], "not covered by") {
		t.Errorf("the warning %q does not say that only the part of the reservation not covered by reported costs is charged", found[0])
	}
}

// On the real store, a run whose agent reported tokens and no cost is
// charged when its reservation is closed, under a cap at what it reserved,
// and not at all with nothing reserved. Either way the Owner reads the same
// warning that some usage had no cost reported.
func TestStatusOnTheRealStoreWarnsAboutAClosedRunWithoutACost(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		settings  budget.Settings
		estimate  core.Micros
		wantSpent core.Micros
	}{
		"capped":                {capped(10*million, 0), million, million},
		"unlimited, no reserve": {budget.Settings{Mode: core.BudgetUnlimited}, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
				st := statusAfter(t, db, c.settings, func(ctx context.Context, gate *budget.Gate, tx state.Tx) error {
					return bookRunWithoutACost(ctx, gate, tx, c.estimate)
				})
				if st.SpentUnverifiedMicros != c.wantSpent || st.ReservedMicros != 0 {
					t.Errorf("status = %+v, want %d unverified and nothing held", st, c.wantSpent)
				}
				unpricedWarning(t, st)
			})
		})
	}
}

// A run that reserved nothing and whose usage had no cost is not charged,
// also when a rollback of the same run later reserves and settles its
// nominal cost: the rollback's reservation stands for the rollback, not for
// the run's usage, and budget.get does not say that usage was charged.
func TestARollbackOfARunThatReservedNothingDoesNotMakeItsUsageCharged(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		const rollbackCost = 1
		st := statusAfter(t, db, budget.Settings{Mode: core.BudgetUnlimited}, func(ctx context.Context, gate *budget.Gate, tx state.Tx) error {
			if err := bookRunWithoutACost(ctx, gate, tx, 0); err != nil {
				return err
			}
			req := request(1, rollbackCost)
			req.FactoryID, req.IdempotencyKey = sqlstoretest.Factory, "rollback/run-1"
			rb, err := gate.ReserveRollback(ctx, tx, req)
			if err != nil {
				return err
			}
			if rb.Amount != rollbackCost || rb.RunID != req.RunID {
				t.Errorf("the rollback reserved %+v, want its own reservation of the run", rb)
			}
			return gate.Settle(ctx, tx, rb.ID, core.Usage{IssueID: req.IssueID, RunID: req.RunID, Cost: rollbackCost})
		})
		if st.SpentUnverifiedMicros != rollbackCost || st.ReservedMicros != 0 {
			t.Errorf("status = %+v, want only the rollback's cost spent", st)
		}
		unpricedWarning(t, st)
	})
}

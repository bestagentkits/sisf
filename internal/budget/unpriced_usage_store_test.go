package budget_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// On the real store, a run whose agent reported tokens and no cost is booked
// as the dispatcher books an attempt and its reservation is closed as the
// factory closes it. The Owner then reads that some usage had no cost
// reported: under a cap it was charged at the reserved amount; with nothing
// reserved it is not counted.
func TestStatusOnTheRealStoreWarnsAboutAClosedRunWithoutACost(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		settings  budget.Settings
		estimate  core.Micros
		wantSpent core.Micros
		want      string
	}{
		"capped":                {capped(10*million, 0), million, million, "charged at the amount reserved"},
		"unlimited, no reserve": {budget.Settings{Mode: core.BudgetUnlimited}, 0, 0, "not counted"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
				ctx := context.Background()
				store := db.Open(t, core.ZoneControl)
				gate := budget.NewGate(budget.Config{Clock: db.Clock})
				err := store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
					for key, v := range map[string]any{
						dispatcher.ConfirmationKey: dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true,
							ConfirmedAt: db.Clock.Now().Add(-time.Hour)},
						budget.SettingsKey: c.settings,
					} {
						raw, err := json.Marshal(v)
						if err != nil {
							return err
						}
						if _, err := tx.PutKV(ctx, state.KVEntry{Namespace: budget.SettingsNamespace, Key: key, Value: raw}); err != nil {
							return err
						}
					}
					req := request(1, c.estimate)
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
					var spend dispatcher.RunSpend
					spend.Add([]core.Usage{u}, true)
					return dispatcher.CloseReservationOfRun(ctx, gate, tx, r.RunID, r.ID, spend)
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
				if st.SpentUnverifiedMicros != c.wantSpent || st.ReservedMicros != 0 {
					t.Errorf("status = %+v, want %d unverified and nothing held", st, c.wantSpent)
				}
				if !warned(st, "no cost reported") || !warned(st, c.want) {
					t.Errorf("warnings = %v, want one that says %q", st.Warnings, c.want)
				}
			})
		})
	}
}

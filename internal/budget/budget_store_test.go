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

// The control zone writes the confirmation and the settings; the gate reads
// them through the transaction of the zone that claims. An unlimited budget
// is admitted from the agent zone; a cap, which needs the usage ledger totals
// only the control zone may read, is enforced there and fails closed in a
// zone that cannot read them.
func TestGateWorksAcrossZonesOnTheRealStore(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		put := func(key string, v any) {
			raw, _ := json.Marshal(v)
			err := db.OpenFactory(t, factory, core.ZoneControl).Update(ctx, func(ctx context.Context, tx state.Tx) error {
				_, err := tx.PutKV(ctx, state.KVEntry{Namespace: budget.SettingsNamespace, Key: key, Value: raw})
				return err
			})
			if err != nil {
				t.Fatalf("control zone cannot write %s: %v", key, err)
			}
		}
		gate := budget.NewGate(budget.Config{Clock: db.Clock})
		reserveIn := func(zone core.Zone, n int, estimate core.Micros) (res core.Reservation, err error) {
			err = db.OpenFactory(t, factory, zone).Update(ctx, func(ctx context.Context, tx state.Tx) (err error) {
				res, err = gate.Reserve(ctx, tx, request(n, estimate))
				return err
			})
			return res, err
		}

		reserve := func(n int, estimate core.Micros) (core.Reservation, error) {
			return reserveIn(core.ZoneControl, n, estimate)
		}
		if _, err := reserveIn(core.ZoneAgent, 1, million); !core.HasCode(err, core.CodeBudgetUnconfirmed) {
			t.Fatalf("an unconfirmed budget admitted a run: %v", err)
		}
		put(dispatcher.ConfirmationKey, dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: db.Clock.Now().Add(-time.Hour)})
		if r, err := reserveIn(core.ZoneAgent, 5, 0); err != nil || r.Mode != core.BudgetUnlimited {
			t.Fatalf("the agent zone could not claim under the confirmed default: %+v, %v", r, err)
		}
		put(budget.SettingsKey, capped(3*million, 0))
		if _, err := reserveIn(core.ZoneAgent, 6, million); !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("a cap was not enforced and not refused in a zone that cannot total usage: %v", err)
		}
		if r, err := reserve(1, 2*million); err != nil || r.Mode != core.BudgetCapped {
			t.Fatalf("reserve = %+v, %v", r, err)
		}
		if _, err := reserve(2, 2*million); !core.HasCode(err, core.CodeBudgetExceeded) {
			t.Errorf("a second reservation past the cap: %v", err)
		}
	})
}

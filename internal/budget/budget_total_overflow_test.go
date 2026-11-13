package budget_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// The store caps each of a period's totals, but what is held and what is
// spent are added in the gate. Two totals near that cap must still fill the
// budget: their sum saturates instead of wrapping to a negative amount that
// would let any estimate fit, and the available amount shows nothing left.
func TestTotalsNearTheStoresCapNeverOpenTheBudget(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		control := db.Open(t, core.ZoneControl)
		for _, kv := range []struct {
			namespace, key string
			value          any
		}{
			{dispatcher.ConfirmationNamespace, dispatcher.ConfirmationKey,
				dispatcher.BudgetConfirmation{Mode: core.BudgetCapped, AlertOff: true, ConfirmedAt: db.Clock.Now().Add(-time.Hour)}},
			{budget.SettingsNamespace, budget.SettingsKey, capped(3*million, 0)},
		} {
			raw, _ := json.Marshal(kv.value)
			if err := control.Update(ctx, func(ctx context.Context, tx state.Tx) error {
				_, err := tx.PutKV(ctx, state.KVEntry{Namespace: kv.namespace, Key: kv.key, Value: raw})
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}

		// A held reservation and a usage row each as large as the store's
		// sums go, written past the store's own bounds.
		const huge = 1 << 62
		at := db.Clock.Now().UnixNano()
		raw := db.Raw(t)
		if db.Dialect == sqlstore.DialectPostgres {
			if _, err := raw.ExecContext(ctx, `ALTER TABLE usage_ledger DROP CONSTRAINT IF EXISTS "sisf_usage_values"`); err != nil {
				t.Fatal(err)
			}
		}
		for _, statement := range []string{
			fmt.Sprintf(`INSERT INTO reservations (factory_id, id, issue, run_id, budget_mode, amount, status, hard_cap_enforced,
				idempotency_key, created_at) VALUES ('%s', 'res-huge', 'acme/app#9', 'run-9', 'capped', %d, 'held', true, 'claim/run-9', %d)`,
				sqlstoretest.Factory, huge, at),
			fmt.Sprintf(`INSERT INTO usage_ledger (factory_id, issue, run_id, reservation_id, step, model_route,
				input_tokens, output_tokens, cost, verified, recorded_at) VALUES ('%s', 'acme/app#9', 'run-9', 'res-huge', 'coding', '{}',
				1, 1, %d, false, %d)`, sqlstoretest.Factory, huge, at),
		} {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("write the oversized budget rows: %v", err)
			}
		}

		gate := budget.NewGate(budget.Config{Clock: db.Clock})
		req := request(1, million)
		req.FactoryID = sqlstoretest.Factory
		err := control.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			_, err := gate.Reserve(ctx, tx, req)
			return err
		})
		if !core.HasCode(err, core.CodeBudgetExceeded) {
			t.Fatalf("a reservation under totals near the store's cap: %v, want BUDGET_EXCEEDED", err)
		}

		var st budget.Status
		if err := control.View(ctx, func(ctx context.Context, tx state.Tx) (err error) {
			st, err = budget.ReadStatus(ctx, dispatcher.KVConfirmation{}, tx, db.Clock.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if st.AvailableMicros == nil || *st.AvailableMicros > 0 {
			t.Errorf("the budget shows %v available, want nothing left", st.AvailableMicros)
		}
	})
}

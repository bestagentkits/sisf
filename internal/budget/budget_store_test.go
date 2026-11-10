package budget_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// The control zone writes the confirmation and the settings; the gate reads
// them through the transaction of the zone that claims. The agent zone, as
// the dispatcher, is admitted under an unlimited budget and under a cap, which
// it enforces against the totals of its own factory's usage ledger: it settles
// usage, is refused once the cap is spent, and never counts the usage of
// another factory. The application zone is refused outright. On PostgreSQL
// the agent zone works over its own role, which reads the ledger and may
// neither change nor remove a recorded row.
func TestGateWorksAcrossZonesOnTheRealStore(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		// The test logins of the zone roles belong to the default test factory.
		factory := sqlstoretest.Factory
		openAgent := func() *sqlstore.Store { return db.OpenFactory(t, factory, core.ZoneAgent) }
		var agentLogin string
		if db.Dialect == sqlstore.DialectPostgres {
			logins := db.ZoneLogins(t)
			agentLogin = logins.DSN[core.ZoneAgent]
			openAgent = func() *sqlstore.Store { return db.OpenWith(t, agentLogin, core.ZoneAgent) }
		}
		agent := openAgent()
		control := db.OpenFactory(t, factory, core.ZoneControl)

		put := func(key string, v any) {
			raw, _ := json.Marshal(v)
			err := control.Update(ctx, func(ctx context.Context, tx state.Tx) error {
				_, err := tx.PutKV(ctx, state.KVEntry{Namespace: budget.SettingsNamespace, Key: key, Value: raw})
				return err
			})
			if err != nil {
				t.Fatalf("control zone cannot write %s: %v", key, err)
			}
		}
		gate := budget.NewGate(budget.Config{Clock: db.Clock})
		reserveIn := func(store *sqlstore.Store, n int, estimate core.Micros) (res core.Reservation, err error) {
			req := request(n, estimate)
			req.FactoryID = factory
			err = store.Update(ctx, func(ctx context.Context, tx state.Tx) (err error) {
				res, err = gate.Reserve(ctx, tx, req)
				return err
			})
			return res, err
		}

		if _, err := reserveIn(agent, 1, million); !core.HasCode(err, core.CodeBudgetUnconfirmed) {
			t.Fatalf("an unconfirmed budget admitted a run: %v", err)
		}
		put(dispatcher.ConfirmationKey, dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: db.Clock.Now().Add(-time.Hour)})
		if r, err := reserveIn(agent, 5, 0); err != nil || r.Mode != core.BudgetUnlimited {
			t.Fatalf("the agent zone could not claim under the confirmed default: %+v, %v", r, err)
		}

		put(budget.SettingsKey, capped(3*million, 0))
		// Usage of another factory sharing the database is not this
		// factory's spending.
		if err := db.OpenFactory(t, "another-factory", core.ZoneControl).Update(ctx, func(ctx context.Context, tx state.Tx) error {
			theirs := usage(1, 100*million, true)
			theirs.FactoryID = "another-factory"
			return tx.AppendUsage(ctx, theirs)
		}); err != nil {
			t.Fatal(err)
		}
		first, err := reserveIn(agent, 6, million)
		if err != nil || first.Mode != core.BudgetCapped || first.Status != core.ReservationHeld {
			t.Fatalf("the agent zone could not reserve under the cap: %+v, %v", first, err)
		}
		if err := agent.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			spent := usage(6, million, true)
			spent.FactoryID = factory
			return gate.Settle(ctx, tx, first.ID, spent)
		}); err != nil {
			t.Fatalf("the agent zone could not settle its usage: %v", err)
		}
		// One million is spent; two remain under the cap.
		if r, err := reserveIn(agent, 7, 2*million); err != nil || r.Mode != core.BudgetCapped {
			t.Fatalf("the agent zone could not reserve what remains of the cap: %+v, %v", r, err)
		}
		if _, err := reserveIn(agent, 8, 1); !core.HasCode(err, core.CodeBudgetExceeded) {
			t.Errorf("the agent zone reserved past an exhausted cap: %v", err)
		}
		if _, err := reserveIn(db.OpenFactory(t, factory, core.ZoneApplication), 9, 1); !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("the application zone reached the budget: %v", err)
		}

		if agentLogin == "" {
			return
		}
		raw, err := sql.Open("pgx", agentLogin)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = raw.Close() })
		for _, statement := range []string{
			`UPDATE usage_ledger SET cost = 0`,
			`DELETE FROM usage_ledger`,
			`TRUNCATE usage_ledger`,
		} {
			_, err := raw.ExecContext(ctx, statement)
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Code != "42501" {
				t.Errorf("the agent role must be refused %q, got %v", statement, err)
			}
		}
		var spent int64
		if err := raw.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost), 0) FROM usage_ledger WHERE factory_id = $1`, string(factory)).Scan(&spent); err != nil || spent != int64(million) {
			t.Errorf("the agent role reads %d of its factory's usage after the refused changes, want %d: %v", spent, million, err)
		}
	})
}

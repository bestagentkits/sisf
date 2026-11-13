package sqlstore_test

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// serverNanos is the database server's clock, in the nanoseconds the store
// dates rows with.
func serverNanos(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var at int64
	if err := db.QueryRowContext(ctx, `SELECT CAST(EXTRACT(EPOCH FROM clock_timestamp()) * 1000000000 AS BIGINT)`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

// insertReservation and insertUsage write a row as a zone role would with
// plain SQL, dated by the writer.
func insertReservation(db *sql.DB, id string, run core.RunID, amount int64, at time.Time) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO reservations (factory_id, id, issue, run_id, budget_mode, amount, status,
		hard_cap_enforced, idempotency_key, created_at) VALUES ('%s', '%s', 'acme/app#7', '%s', 'capped', %d, 'held', true, 'claim-%s', %d)`,
		sqlstoretest.Factory, id, run, amount, id, at.UnixNano()))
	return err
}

func insertUsage(db *sql.DB, reservation string, run core.RunID, cost int64, verified bool, at time.Time) error {
	return insertUsageOf(db, sqlstoretest.Factory, "acme/app#7", reservation, run, cost, verified, at)
}

// insertUsageOf writes a usage row of the given factory and issue.
func insertUsageOf(db *sql.DB, factory core.FactoryID, issue, reservation string, run core.RunID, cost int64, verified bool, at time.Time) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO usage_ledger (factory_id, issue, run_id, reservation_id, step, model_route,
		input_tokens, output_tokens, cost, verified, recorded_at) VALUES ('%s', '%s', '%s', '%s', 'coding', '{}', 1, 1, %d, %t, %d)`,
		factory, issue, run, reservation, cost, verified, at.UnixNano()))
	return err
}

// Over their own logins, past the store, the zone roles get the database's
// clock on every reservation and usage row they write, whatever date they
// give, and the agent role books unverified usage only against a
// reservation of the same run and issue. The reviewer's probe, a
// reservation dated 1999 and a row of its whole amount dated 2000, closes
// with the amount counted in the current budget period.
func TestPostgresZoneRolesDateBudgetRowsByTheServerAndBookOnlyTheirOwnUsage(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		assertBudgetRowRules(t, db, logins)
	})
}

// The migration writes the same rules on a store whose roles an earlier
// release granted, keeping the roles it finds.
func TestPostgresMigrationDatesBudgetRowsByTheServerOnAGrantedStore(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		raw := db.Raw(t)
		// The ledger and the reservations as the earlier release left them.
		for _, statement := range []string{
			`DROP TRIGGER IF EXISTS "sisf_budget_row_time" ON reservations`,
			`DROP TRIGGER IF EXISTS "sisf_budget_row_time" ON usage_ledger`,
			`DROP INDEX IF EXISTS "usage_ledger_run"`,
			`ALTER POLICY "sisf_agent_insert" ON usage_ledger WITH CHECK (true)`,
			`ALTER POLICY "sisf_control_insert" ON usage_ledger WITH CHECK (true)`,
		} {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("restore the earlier schema: %v", err)
			}
		}
		agent := rawLogin(t, logins.DSN[core.ZoneAgent])
		if err := insertUsage(agent, "", "run-9", 1, true, time.Unix(0, 0)); err != nil {
			t.Fatalf("the premise of this test: the earlier schema lets the agent role book verified usage: %v", err)
		}
		script, err := sqlstore.MigrationScript(13, sqlstore.DialectPostgres)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.ExecContext(ctx, script); err != nil {
			t.Fatalf("apply the migration to a granted store: %v", err)
		}
		assertBudgetRowRules(t, db, logins)
	})
}

// assertBudgetRowRules checks the database's rules on the reservations and
// the usage ledger over the zones' own logins.
func assertBudgetRowRules(t *testing.T, db *sqlstoretest.Database, logins sqlstoretest.ZoneLogins) {
	t.Helper()
	agent, control := rawLogin(t, logins.DSN[core.ZoneAgent]), rawLogin(t, logins.DSN[core.ZoneControl])
	longAgo, then := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	before := serverNanos(t, db.Raw(t))
	if err := insertReservation(agent, "r-probe", "run-1", 1_000_000, longAgo); err != nil {
		t.Fatalf("the agent role makes the reservation of its claim: %v", err)
	}
	if err := insertUsage(agent, "r-probe", "run-1", 1_000_000, false, then); err != nil {
		t.Fatalf("the agent role books usage against its run's reservation: %v", err)
	}
	var created, recorded int64
	if err := db.Raw(t).QueryRowContext(ctx, `SELECT r.created_at, u.recorded_at FROM reservations r JOIN usage_ledger u
		ON u.reservation_id = r.id WHERE r.id = 'r-probe'`).Scan(&created, &recorded); err != nil {
		t.Fatal(err)
	}
	if created < before || recorded < created {
		t.Fatalf("the rows are dated %d and %d, want the server's clock from %d on", created, recorded, before)
	}

	for name, write := range map[string]func() error{
		"the agent role books verified usage":               func() error { return insertUsage(agent, "r-probe", "run-1", 5, true, then) },
		"the agent role books usage against no reservation": func() error { return insertUsage(agent, "", "run-1", 5, false, then) },
		"the agent role books usage against another run's":  func() error { return insertUsage(agent, "r-probe", "run-2", 5, false, then) },
		"the agent role books usage against an unknown one": func() error { return insertUsage(agent, "r-none", "run-1", 5, false, then) },
		"the agent role books a negative cost":              func() error { return insertUsage(agent, "r-probe", "run-1", -1e18, false, then) },
		// Each names the probe's reservation and run and differs in one
		// column only, so each clause of the agent role's policy is tried.
		"the agent role books usage of another issue": func() error {
			return insertUsageOf(agent, sqlstoretest.Factory, "acme/app#8", "r-probe", "run-1", 5, false, then)
		},
		"the agent role books usage of another factory": func() error {
			return insertUsageOf(agent, "another-factory", "acme/app#7", "r-probe", "run-1", 5, false, then)
		},
		"the control role books verified usage": func() error { return insertUsage(control, "", "run-1", 5, true, then) },
	} {
		if err := write(); err == nil {
			t.Errorf("%s: the database let it through", name)
		}
	}

	// The reviewer's probe, closed by the control plane: the row counts
	// for the run and in the current budget period.
	store := db.OpenWith(t, logins.DSN[core.ZoneControl], core.ZoneControl)
	mustUpdate(t, store, func(tx state.Tx) error {
		r, err := tx.GetReservation(ctx, "r-probe")
		if err != nil {
			return err
		}
		booked, err := tx.RunUsage(ctx, "run-1", r.CreatedAt)
		if err != nil {
			return err
		}
		if booked.Cost != 1_000_000 || booked.Entries != 1 {
			return fmt.Errorf("the run's usage since its reservation is %+v, want the probe row", booked)
		}
		return nil
	})
	if got := totals(t, store, time.Unix(0, before)); got.SpentUnverified < 1_000_000 {
		t.Fatalf("the current period's totals are %+v, want the probe row counted", got)
	}
}

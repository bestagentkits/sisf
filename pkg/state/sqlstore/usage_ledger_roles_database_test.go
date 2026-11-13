package sqlstore_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// The agent zone totals the usage of its own factory, which a capped budget
// needs to admit a run, and never that of another factory sharing the
// database: the store reads only the rows of the factory it was opened for.
func TestAgentZoneTotalsTheUsageOfItsOwnFactoryOnly(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		rawUsage(t, db, "run-1", 5, true, db.Clock.Now())
		mustUpdate(t, db.Open(t, core.ZoneControl), func(tx state.Tx) error { return tx.PutReservation(ctx, reservation("r-1", 1)) })
		mustUpdate(t, db.OpenFactory(t, "another-factory", core.ZoneControl), func(tx state.Tx) error {
			return tx.AppendUsage(ctx, spend(100, false, db.Clock.Now()))
		})
		agent := db.Open(t, core.ZoneAgent)
		mustUpdate(t, agent, func(tx state.Tx) error { return tx.AppendUsage(ctx, spend(2, false, db.Clock.Now())) })
		if got := totals(t, agent, db.Clock.Now().Add(-1)); got.SpentVerified != 5 || got.SpentUnverified != 2 {
			t.Fatalf("the agent zone totals %+v, want 5 verified and 2 unverified of its own factory", got)
		}
		// Usage is still recorded only for the store's own factory.
		other := spend(1, false, db.Clock.Now())
		other.FactoryID = "another-factory"
		wantCode(t, update(agent, func(tx state.Tx) error { return tx.AppendUsage(ctx, other) }), core.CodeInvalidArgument)
	})
}

// Over its own login the agent role reads and appends usage, and the
// database refuses it any change to a recorded row.
func TestPostgresAgentRoleReadsTheUsageLedgerAndChangesNoRow(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		assertUsageLedgerRoles(t, db, logins)
	})
}

// The migration grants the read and writes the ledger's policies on a store
// whose roles an earlier release granted, keeping the roles it finds, so
// the agent zone may total usage from the moment the store is migrated and
// every zone keeps metering it under row-level security.
func TestPostgresMigrationLetsTheAgentRoleOfAGrantedStoreReadTheUsageLedger(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		logins := db.ZoneLogins(t)
		raw := db.Raw(t)
		agentRole := logins.Roles[core.ZoneAgent]
		// The ledger as the earlier release left it: no row security, no
		// policies and no read for the agent role.
		for _, statement := range []string{
			`ALTER TABLE usage_ledger DISABLE ROW LEVEL SECURITY`,
			`DROP POLICY "sisf_agent_select" ON usage_ledger`,
			`DROP POLICY "sisf_agent_insert" ON usage_ledger`,
			`DROP POLICY "sisf_control_select" ON usage_ledger`,
			`DROP POLICY "sisf_control_insert" ON usage_ledger`,
			`REVOKE SELECT ON usage_ledger FROM ` + agentRole,
		} {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("restore the earlier ledger: %v", err)
			}
		}
		agent := rawLogin(t, logins.DSN[core.ZoneAgent])
		refusedRow(t, agent, "a read of the ledger before the migration, the premise of this test", `SELECT count(*) FROM usage_ledger`)

		script, err := sqlstore.MigrationScript(6, sqlstore.DialectPostgres)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.ExecContext(ctx, script); err != nil {
			t.Fatalf("apply the migration to a granted store: %v", err)
		}
		for zone, policy := range map[core.Zone]string{core.ZoneAgent: "sisf_agent_select", core.ZoneControl: "sisf_control_insert"} {
			var roles string
			if err := raw.QueryRowContext(ctx, `SELECT array_to_string(roles, ',') FROM pg_policies
				WHERE schemaname = current_schema() AND tablename = 'usage_ledger' AND policyname = $1`, policy).Scan(&roles); err != nil {
				t.Fatalf("read the policy %s: %v", policy, err)
			}
			if roles != logins.Roles[zone] {
				t.Errorf("the policy %s names %q, want the %s role %q", policy, roles, zone, logins.Roles[zone])
			}
		}
		assertUsageLedgerRoles(t, db, logins)
	})
}

// assertUsageLedgerRoles checks what each zone role may do to the usage
// ledger over its own login, past the store.
func assertUsageLedgerRoles(t *testing.T, db *sqlstoretest.Database, logins sqlstoretest.ZoneLogins) {
	t.Helper()
	mustUpdate(t, db.OpenWith(t, logins.DSN[core.ZoneControl], core.ZoneControl), func(tx state.Tx) error {
		if err := tx.PutReservation(ctx, reservation("r-1", 1)); err != nil {
			return err
		}
		return tx.AppendUsage(ctx, spend(7, false, db.Clock.Now()))
	})
	agentStore := db.OpenWith(t, logins.DSN[core.ZoneAgent], core.ZoneAgent)
	mustUpdate(t, agentStore, func(tx state.Tx) error { return tx.AppendUsage(ctx, spend(3, false, db.Clock.Now())) })
	if got := totals(t, agentStore, db.Clock.Now().Add(-1)); got.SpentVerified != 0 || got.SpentUnverified != 10 {
		t.Fatalf("the agent role totals %+v, want 10 unverified", got)
	}

	agent := rawLogin(t, logins.DSN[core.ZoneAgent])
	for name, statement := range map[string]string{
		"a change of the recorded cost": `UPDATE usage_ledger SET cost = 0`,
		"a removal of recorded usage":   `DELETE FROM usage_ledger`,
		"a truncation of the ledger":    `TRUNCATE usage_ledger`,
	} {
		refusedRow(t, agent, name, statement)
	}
	var rows, cost int64
	if err := agent.QueryRowContext(ctx, `SELECT count(*), COALESCE(SUM(cost), 0) FROM usage_ledger`).Scan(&rows, &cost); err != nil {
		t.Fatalf("the agent role must read the ledger: %v", err)
	}
	if rows != 2 || cost != 10 {
		t.Errorf("after the refused changes the agent role reads %d rows costing %d, want 2 costing 10", rows, cost)
	}

	application := rawLogin(t, logins.DSN[core.ZoneApplication])
	for _, statement := range []string{
		`SELECT count(*) FROM usage_ledger`,
		`INSERT INTO usage_ledger (factory_id, issue, run_id, reservation_id, step, model_route,
			input_tokens, output_tokens, cost, verified, recorded_at) VALUES ('f', '', '', '', '', '{}', 0, 0, 0, true, 0)`,
	} {
		_, err := application.ExecContext(ctx, statement)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "42501" {
			t.Errorf("the application role must be refused %q, got %v", statement, err)
		}
	}
}

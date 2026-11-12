package sqlstore_test

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

func TestGrantingRolesIsRefusedWhereItCannotBeDone(t *testing.T) {
	t.Parallel()
	roles := sqlstore.ZoneRoles{
		core.ZoneApplication: "sisf_application", core.ZoneAgent: "sisf_agent", core.ZoneControl: "sisf_control",
	}
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		// A zone other than control never changes who may reach the store.
		wantCode(t, db.Open(t, core.ZoneAgent).GrantZoneRoles(ctx, roles), core.CodePermissionDenied)
		wantCode(t, db.Open(t, core.ZoneApplication).GrantZoneRoles(ctx, roles), core.CodePermissionDenied)
		if db.Dialect == sqlstore.DialectSQLite {
			// SQLite has no roles, and the store says so instead of pretending.
			wantCode(t, db.Open(t, core.ZoneControl).GrantZoneRoles(ctx, roles), core.CodeNotSupported)
		}
	})
}

// The database itself must keep the zones apart, whatever the code of a
// compromised zone does. This needs a PostgreSQL connection that may create
// roles and act as them; it is skipped otherwise.
func TestPostgresRolesKeepTheZonesApartInTheDatabase(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		if db.Dialect != sqlstore.DialectPostgres {
			t.Skip("database roles exist on PostgreSQL only")
		}
		control := db.Open(t, core.ZoneControl)
		mustUpdate(t, control, func(tx state.Tx) error { return tx.AppendUsage(ctx, spend(1, true, db.Clock.Now())) })
		if _, err := control.AppendAuditRow(ctx, func(int64, string) (string, []byte, error) { return "h", []byte("{}"), nil }); err != nil {
			t.Fatal(err)
		}

		raw := db.Raw(t)
		suffix := make([]byte, 6)
		if _, err := rand.Read(suffix); err != nil {
			t.Fatal(err)
		}
		roles := sqlstore.ZoneRoles{}
		for zone, prefix := range map[core.Zone]string{
			core.ZoneApplication: "sisf_t_app_", core.ZoneAgent: "sisf_t_agent_", core.ZoneControl: "sisf_t_control_",
		} {
			role := prefix + hex.EncodeToString(suffix)
			if _, err := raw.ExecContext(ctx, `CREATE ROLE `+role); err != nil {
				t.Skipf("the test connection cannot create roles: %v", err)
			}
			t.Cleanup(func() {
				_, _ = raw.ExecContext(ctx, `DROP OWNED BY `+role)
				_, _ = raw.ExecContext(ctx, `DROP ROLE `+role)
			})
			roles[zone] = role
		}
		if err := control.GrantZoneRoles(ctx, roles); err != nil {
			t.Fatalf("grant zone roles: %v", err)
		}
		// Granting again is safe and leaves the same privileges.
		if err := control.GrantZoneRoles(ctx, roles); err != nil {
			t.Fatalf("grant zone roles again: %v", err)
		}

		for _, check := range []struct {
			zone      core.Zone
			statement string
			allowed   bool
		}{
			{core.ZoneApplication, `SELECT version FROM schema_migrations`, true},
			{core.ZoneApplication, `SELECT * FROM records`, false},
			{core.ZoneApplication, `SELECT * FROM kv`, false},
			{core.ZoneApplication, `SELECT * FROM audit_log`, false},
			{core.ZoneApplication, `INSERT INTO inbox (factory_id, source, delivery_id, event_type, payload, received_at, processed_at) VALUES ('f', 's', 'd', '', '', 0, 0)`, false},
			{core.ZoneApplication, `UPDATE leases SET holder = 'intruder'`, false},
			{core.ZoneApplication, `SELECT * FROM usage_ledger`, false},
			{core.ZoneAgent, `SELECT * FROM records`, true},
			{core.ZoneAgent, `SELECT * FROM inbox`, false},
			{core.ZoneAgent, `SELECT * FROM outbox`, false},
			{core.ZoneAgent, `SELECT * FROM audit_log`, false},
			{core.ZoneAgent, `SELECT * FROM kv`, true},
			{core.ZoneAgent, `INSERT INTO kv (factory_id, namespace, entry_key, entry_value, version, updated_at) VALUES ('f', 'policy', 'baseline', '', 1, 0)`, false},
			{core.ZoneAgent, `INSERT INTO kv (factory_id, namespace, entry_key, entry_value, version, updated_at) VALUES ('f', 'dispatcher', 'run/r', '', 1, 0)`, true},
			{core.ZoneAgent, `INSERT INTO inbox (factory_id, source, delivery_id, event_type, payload, received_at, processed_at) VALUES ('f', 's', 'd', '', '', 0, 0)`, false},
			{core.ZoneAgent, `UPDATE audit_log SET hash = hash`, false},
			{core.ZoneAgent, `DELETE FROM audit_log`, false},
			{core.ZoneAgent, `TRUNCATE audit_log`, false},
			{core.ZoneAgent, `SELECT * FROM usage_ledger`, true},
			{core.ZoneAgent, `UPDATE usage_ledger SET cost = 0`, false},
			{core.ZoneAgent, `DELETE FROM usage_ledger`, false},
			{core.ZoneAgent, `TRUNCATE usage_ledger`, false},
			{core.ZoneAgent, `DROP TABLE records`, false},
			{core.ZoneControl, `INSERT INTO inbox (factory_id, source, delivery_id, event_type, payload, received_at, processed_at) VALUES ('f', 's', 'd', '', '', 0, 0)`, true},
			{core.ZoneControl, `UPDATE audit_log SET hash = hash`, false},
			{core.ZoneControl, `DELETE FROM audit_log`, false},
			{core.ZoneControl, `UPDATE usage_ledger SET cost = 0`, false},
		} {
			err := runAs(raw, roles[check.zone], check.statement)
			switch {
			case check.allowed && err != nil:
				t.Errorf("%s role: %q must be allowed, got %v", check.zone, check.statement, err)
			case !check.allowed && (err == nil || !strings.Contains(err.Error(), "42501")):
				t.Errorf("%s role: %q must be denied by the database, got %v", check.zone, check.statement, err)
			}
		}
	})
}

// runAs runs one statement as role and undoes it again.
func runAs(raw *sql.DB, role, statement string) error {
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL ROLE `+role); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, statement)
	return err
}

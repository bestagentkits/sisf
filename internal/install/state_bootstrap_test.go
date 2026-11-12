package install

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

func sqliteConfig(t *testing.T, budget *BudgetConfirmation) BootstrapConfig {
	t.Helper()
	return BootstrapConfig{
		FactoryID: "acme-factory", Dialect: sqlstore.DialectSQLite,
		DSN: filepath.Join(t.TempDir(), "state", "factory.db"), Budget: budget,
		Clock: sqlstoretest.NewClock(fixedStart),
	}
}

func openControl(t *testing.T, cfg BootstrapConfig) *sqlstore.Store {
	t.Helper()
	store, err := sqlstore.Open(t.Context(), sqlstore.Config{
		Dialect: cfg.Dialect, DSN: cfg.DSN, FactoryID: cfg.FactoryID, Zone: core.ZoneControl, Clock: cfg.Clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestBootstrapMigratesAndStoresTheBudgetConfirmationWhereTheGateReadsIt(t *testing.T) {
	t.Parallel()
	rec := BudgetConfirmation{Mode: core.BudgetUnlimited, AlertThresholdMicros: 50_000_000, ConfirmedAt: fixedStart.UTC()}
	cfg := sqliteConfig(t, &rec)
	if err := Bootstrap(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadBudgetConfirmation(t.Context(), openControl(t, cfg))
	if err != nil || !ok || got.AlertThresholdMicros != 50_000_000 {
		t.Fatalf("confirmation = %+v, %v, %v", got, ok, err)
	}
}

func TestBootstrapWithoutAConfirmationStoresNone(t *testing.T) {
	t.Parallel()
	cfg := sqliteConfig(t, nil)
	if err := Bootstrap(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ReadBudgetConfirmation(t.Context(), openControl(t, cfg)); err != nil || ok {
		t.Fatalf("confirmation present = %v (%v); the gate must keep refusing claims", ok, err)
	}
}

func TestBootstrapCanRunAgainAndReplacesTheConfirmation(t *testing.T) {
	t.Parallel()
	first := BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: fixedStart.UTC()}
	cfg := sqliteConfig(t, &first)
	for range 2 {
		if err := Bootstrap(t.Context(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Budget = &BudgetConfirmation{Mode: core.BudgetUnlimited, AlertThresholdMicros: 7_000_000, ConfirmedAt: fixedStart.UTC()}
	if err := Bootstrap(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	got, _, _ := ReadBudgetConfirmation(t.Context(), openControl(t, cfg))
	if got.AlertOff || got.AlertThresholdMicros != 7_000_000 {
		t.Fatalf("confirmation after a second run = %+v", got)
	}
}

// postgresBase is a database of the throwaway server named by the test
// environment, or the test is skipped. See the package comment for how the
// server is started on port 7469.
func postgresBase(t *testing.T) *url.URL {
	t.Helper()
	dsn := os.Getenv(sqlstoretest.EnvPostgresDSN)
	if dsn == "" {
		t.Skip(sqlstoretest.EnvPostgresDSN + " is not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestBootstrapGivesEachZoneItsOwnRoleOnPostgres(t *testing.T) {
	base := postgresBase(t)
	ctx := context.Background()
	admin, err := sql.Open("pgx", base.String())
	if err != nil {
		t.Fatal(err)
	}
	// Closed after the cleanup below has dropped what the test made: a
	// deferred close would run first and leave the database and roles behind.
	t.Cleanup(func() { _ = admin.Close() })
	suffix := strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))[:20]
	database := "sisf_boot_" + suffix
	roles := map[core.Zone]string{
		core.ZoneApplication: "t_app_" + suffix, core.ZoneAgent: "t_agent_" + suffix, core.ZoneControl: "t_ctl_" + suffix,
	}
	pws := map[core.Zone]string{core.ZoneApplication: "pw-app", core.ZoneAgent: "pw-agent", core.ZoneControl: "pw-ctl"}
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+database); err != nil {
		t.Fatalf("create the test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+database+` WITH (FORCE)`)
		for _, r := range roles {
			_, _ = admin.ExecContext(ctx, `DROP ROLE IF EXISTS `+r)
		}
	})
	dbURL := *base
	dbURL.Path = "/" + database
	cfg := BootstrapConfig{
		FactoryID: "acme-factory", Dialect: sqlstore.DialectPostgres, DSN: dbURL.String(),
		Roles: roles, Passwords: pws, Clock: sqlstoretest.NewClock(fixedStart),
	}
	for range 2 { // the second run must change nothing but rotate passwords
		if err := Bootstrap(ctx, cfg); err != nil {
			t.Fatal(err)
		}
	}

	as := func(zone core.Zone) *sql.DB {
		u := dbURL
		u.User = url.UserPassword(roles[zone], pws[zone])
		db, err := sql.Open("pgx", u.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	var n int
	if err := as(core.ZoneApplication).QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("Zone 1 cannot read the schema version: %v", err)
	}
	for _, table := range []string{"records", "inbox", "audit_log", "kv", "leases"} {
		if err := as(core.ZoneApplication).QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err == nil {
			t.Errorf("Zone 1 can read %s", table)
		}
	}
	if err := as(core.ZoneAgent).QueryRowContext(ctx, `SELECT count(*) FROM records`).Scan(&n); err != nil {
		t.Errorf("Zone 2 cannot read the work records: %v", err)
	}
	if _, err := as(core.ZoneAgent).ExecContext(ctx, `DELETE FROM audit_log`); err == nil {
		t.Error("Zone 2 can delete audit records")
	}
	// A zone's password opens its own role only.
	other := dbURL
	other.User = url.UserPassword(roles[core.ZoneControl], pws[core.ZoneApplication])
	if db, err := sql.Open("pgx", other.String()); err == nil {
		defer func() { _ = db.Close() }()
		if db.PingContext(ctx) == nil {
			t.Error("Zone 1's password opened the control zone's role")
		}
	}
}

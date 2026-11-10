// Package sqlstoretest gives tests a real operational store: SQLite in a
// temporary directory always, and PostgreSQL as well when a server is named
// by the environment. Each test gets a database of its own, so tests can run
// in parallel, and a clock that only the test moves.
package sqlstoretest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// EnvPostgresDSN names the PostgreSQL server tests may use. When it is unset
// the PostgreSQL run of every test is skipped; nothing starts a server.
const EnvPostgresDSN = "SISF_TEST_PG_DSN"

// Factory is the factory the test stores belong to unless a test asks for another.
const Factory core.FactoryID = "factory-under-test"

// postgresSlots limits how many PostgreSQL tests of a package run at once.
var postgresSlots = make(chan struct{}, 4)

// Database is one empty or migrated database reserved for a single test.
type Database struct {
	Dialect sqlstore.Dialect
	// DSN is what sqlstore.Config.DSN takes for this database.
	DSN string
	// Clock is shared by every store opened on the database.
	Clock *Clock
}

// Run calls fn once per available dialect, as parallel subtests, with a
// database whose schema is already migrated.
func Run(t *testing.T, fn func(t *testing.T, db *Database)) {
	t.Helper()
	RunUnmigrated(t, func(t *testing.T, db *Database) {
		admin := db.Open(t, core.ZoneControl)
		if err := admin.Migrate(context.Background(), state.MigrateOptions{}); err != nil {
			t.Fatalf("migrate the test database: %v", err)
		}
		fn(t, db)
	})
}

// RunUnmigrated is Run with a database that has no schema yet.
func RunUnmigrated(t *testing.T, fn func(t *testing.T, db *Database)) {
	t.Helper()
	start := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()
		fn(t, &Database{
			Dialect: sqlstore.DialectSQLite,
			DSN:     filepath.Join(t.TempDir(), "state.db"),
			Clock:   NewClock(start),
		})
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv(EnvPostgresDSN)
		if dsn == "" {
			t.Skip(EnvPostgresDSN + " is not set")
		}
		t.Parallel()
		// Every store keeps a few connections; bound how many tests hold
		// them at once so a default server is not run out of connections.
		postgresSlots <- struct{}{}
		t.Cleanup(func() { <-postgresSlots })
		fn(t, &Database{
			Dialect: sqlstore.DialectPostgres,
			DSN:     postgresSchema(t, dsn),
			Clock:   NewClock(start),
		})
	})
}

// Open opens the database as zone for the default factory. The store is
// closed when the test ends.
func (d *Database) Open(t testing.TB, zone core.Zone) *sqlstore.Store {
	t.Helper()
	return d.OpenFactory(t, Factory, zone)
}

// OpenFactory opens the database as zone for the given factory.
func (d *Database) OpenFactory(t testing.TB, factory core.FactoryID, zone core.Zone) *sqlstore.Store {
	t.Helper()
	store, err := sqlstore.Open(context.Background(), sqlstore.Config{
		Dialect: d.Dialect, DSN: d.DSN, FactoryID: factory, Zone: zone, Clock: d.Clock,
	})
	if err != nil {
		t.Fatalf("open %s store as %s: %v", d.Dialect, zone, err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// Raw connects straight to the database, past the store and its checks. It
// stands for someone who reaches the data without going through the factory.
func (d *Database) Raw(t testing.TB) *sql.DB {
	t.Helper()
	driver, dsn := "pgx", d.DSN
	if d.Dialect == sqlstore.DialectSQLite {
		driver, dsn = "sqlite", d.DSN+"?_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatalf("open a raw connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// DisableAppendOnlyGuards removes the database triggers that reject changes
// to an append-only table, as an intruder who owns the database could. Tests
// use it to prove that tampering is still detected afterwards.
func (d *Database) DisableAppendOnlyGuards(t testing.TB, table string) {
	t.Helper()
	ctx := context.Background()
	raw := d.Raw(t)
	if d.Dialect == sqlstore.DialectPostgres {
		if _, err := raw.ExecContext(ctx, `ALTER TABLE `+table+` DISABLE TRIGGER USER`); err != nil {
			t.Fatalf("disable triggers on %s: %v", table, err)
		}
		return
	}
	rows, err := raw.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ?`, table)
	if err != nil {
		t.Fatalf("list triggers on %s: %v", table, err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("read trigger name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("list triggers on %s: %v", table, err)
	}
	for _, name := range names {
		if _, err := raw.ExecContext(ctx, `DROP TRIGGER `+name); err != nil {
			t.Fatalf("drop trigger %s: %v", name, err)
		}
	}
}

// postgresSchema creates a schema used by this test alone and returns a DSN
// that selects it. The schema is dropped when the test ends.
func postgresSchema(t *testing.T, dsn string) string {
	t.Helper()
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("name a test schema: %v", err)
	}
	schema := "sisf_test_" + hex.EncodeToString(suffix)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("connect to PostgreSQL: %v", err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		_ = admin.Close()
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	return withSearchPath(dsn, schema)
}

// withSearchPath makes schema the only schema a connection resolves names in.
// It handles both the URL and the keyword/value form of a connection string.
func withSearchPath(dsn, schema string) string {
	if !strings.Contains(dsn, "://") {
		return dsn + " search_path=" + schema
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

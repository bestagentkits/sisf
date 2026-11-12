package sqlstore_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// Migrators of one schema exclude each other, so a schema is migrated once
// however many start together.
func TestConcurrentMigratorsOfOneSchemaApplyEachMigrationOnce(t *testing.T) {
	t.Parallel()
	sqlstoretest.RunUnmigrated(t, func(t *testing.T, db *sqlstoretest.Database) {
		stores := []*sqlstore.Store{db.Open(t, core.ZoneControl), db.Open(t, core.ZoneControl), db.Open(t, core.ZoneControl)}
		var wg sync.WaitGroup
		errs := make([]error, len(stores))
		for i, s := range stores {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = s.Migrate(ctx, state.MigrateOptions{})
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			t.Fatalf("a concurrent migrator failed: %v", err)
		}
		if v, err := stores[0].SchemaVersion(ctx); err != nil || v != len(sqlstore.Migrations()) {
			t.Fatalf("version = %d, %v", v, err)
		}
	})
}

// The migration lock belongs to one schema: a migration held up in one
// schema does not hold up the migration of another in the same database.
func TestTheMigrationLockOfOneSchemaDoesNotBlockAnother(t *testing.T) {
	t.Parallel()
	sqlstoretest.RunUnmigrated(t, func(t *testing.T, db *sqlstoretest.Database) {
		if db.Dialect != sqlstore.DialectPostgres {
			t.Skip("only PostgreSQL takes a migration lock")
		}
		raw := db.Raw(t)
		lock, class := sqlstore.MigrationLock()
		var own string
		if err := raw.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&own); err != nil || own == "" {
			t.Fatalf("the test database has no schema of its own: %q, %v", own, err)
		}
		other := "sisf_lock_other_" + time.Now().Format("150405999999")
		if _, err := raw.ExecContext(ctx, `CREATE SCHEMA `+other); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = raw.ExecContext(context.Background(), `DROP SCHEMA `+other+` CASCADE`) })

		// A migrator of another schema holds that schema's lock.
		holder, err := raw.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback() }()
		if _, err := holder.ExecContext(ctx, `SET LOCAL search_path TO `+other); err != nil {
			t.Fatal(err)
		}
		if _, err := holder.ExecContext(ctx, lock, class); err != nil {
			t.Fatal(err)
		}
		s := db.Open(t, core.ZoneControl)
		if err := s.Migrate(ctx, state.MigrateOptions{}); err != nil {
			t.Fatalf("the migration waited for another schema's lock: %v", err)
		}

		// A migrator of this schema waits while this schema's lock is held.
		if _, err := holder.ExecContext(ctx, `SET LOCAL search_path TO `+own); err != nil {
			t.Fatal(err)
		}
		if _, err := holder.ExecContext(ctx, lock, class); err != nil {
			t.Fatal(err)
		}
		sqlstore.AddReleaseMigrations(s, addHint)
		short, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := s.Migrate(short, state.MigrateOptions{}); err == nil {
			t.Fatal("a migration went ahead while another migrator of the schema held its lock")
		}
		if err := holder.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := s.Migrate(ctx, state.MigrateOptions{}); err != nil {
			t.Fatalf("the migration did not go ahead once the lock was free: %v", err)
		}
	})
}

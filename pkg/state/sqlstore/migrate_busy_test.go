package sqlstore_test

import (
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// A SQLite migrator that waits longer for the write lock than its busy
// timeout allows does not fail: it tries again until the holder is done,
// and then migrates the schema once.
func TestASQLiteMigratorWaitsOutAWriterThatHoldsTheLockPastItsBusyTimeout(t *testing.T) {
	t.Parallel()
	sqlstoretest.RunUnmigrated(t, func(t *testing.T, db *sqlstoretest.Database) {
		if db.Dialect != sqlstore.DialectSQLite {
			t.Skip("only SQLite gives up on a lock after its busy timeout")
		}
		s := db.Open(t, core.ZoneControl)
		if err := sqlstore.SetBusyTimeout(s, 10*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		holder, err := db.Raw(t).Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Close() }()
		if _, err := holder.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
			t.Fatal(err)
		}

		done := make(chan error, 1)
		go func() { done <- s.Migrate(ctx, state.MigrateOptions{}) }()
		select {
		case err := <-done:
			t.Fatalf("the migrator returned while another connection held the write lock: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
		if _, err := holder.ExecContext(ctx, `COMMIT`); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatalf("the migrator failed once the lock was free: %v", err)
		}
		if v, err := s.SchemaVersion(ctx); err != nil || v != len(sqlstore.Migrations()) {
			t.Fatalf("version = %d, %v", v, err)
		}
	})
}

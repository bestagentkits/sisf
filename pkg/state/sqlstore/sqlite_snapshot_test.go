package sqlstore_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// A snapshot is a database of its own, readable by its owner only, that
// holds the schema and rows of the store as they were: a store opened on it
// finds the record written before and not the one written after. Only the
// control zone takes one, an existing file is never overwritten, and a
// PostgreSQL store is refused.
func TestASQLiteSnapshotIsTheStoreAsItWas(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		path := filepath.Join(t.TempDir(), "backups", "state.db")
		if db.Dialect == sqlstore.DialectPostgres {
			wantCode(t, control.SnapshotSQLite(ctx, path), core.CodeInvalidArgument)
			return
		}
		wantCode(t, db.Open(t, core.ZoneAgent).SnapshotSQLite(ctx, path), core.CodePermissionDenied)
		mustTransition(t, control, received(issue(1)))
		if err := control.SnapshotSQLite(ctx, path); err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		mustTransition(t, control, received(issue(2)))
		wantCode(t, control.SnapshotSQLite(ctx, path), core.CodeConflict)

		for file, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
			info, err := os.Stat(file)
			if err != nil || info.Mode().Perm() != want {
				t.Fatalf("%s: mode %v, %v; want %v", file, info.Mode().Perm(), err, want)
			}
		}
		snapshot := (&sqlstoretest.Database{Dialect: sqlstore.DialectSQLite, DSN: path, Clock: db.Clock}).Open(t, core.ZoneControl)
		if version, err := snapshot.SchemaVersion(ctx); err != nil || version != len(sqlstore.Migrations()) {
			t.Fatalf("the snapshot is at version %d, %v", version, err)
		}
		record(t, snapshot, issue(1))
		wantCode(t, view(snapshot, func(tx state.Tx) error {
			_, err := tx.GetRecord(ctx, issue(2))
			return err
		}), core.CodeNotFound)
	})
}

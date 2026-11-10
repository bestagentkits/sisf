package sqlstore_test

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// A later release adds a column, then a still later one drops it again. The
// first change leaves the previous release working; the second does not.
var (
	addNote  = sqlstore.TestMigration{Name: "add a note to documents", BackwardCompatible: true, SQL: `ALTER TABLE kv ADD COLUMN note TEXT`}
	dropNote = sqlstore.TestMigration{Name: "drop the note from documents", SQL: `ALTER TABLE kv DROP COLUMN note`}
	addHint  = sqlstore.TestMigration{Name: "add a hint to documents", BackwardCompatible: true, SQL: `ALTER TABLE kv ADD COLUMN hint TEXT`}
)

func TestReleaseMigrationsAreNumberedNamedAndScriptedForEveryDialect(t *testing.T) {
	t.Parallel()
	shipped := sqlstore.Migrations()
	if len(shipped) == 0 {
		t.Fatal("a release ships at least the initial schema")
	}
	if err := state.ValidateMigrations(shipped); err != nil {
		t.Fatal(err)
	}
	for _, m := range shipped {
		for _, dialect := range []sqlstore.Dialect{sqlstore.DialectSQLite, sqlstore.DialectPostgres} {
			if script, err := sqlstore.MigrationScript(m.Version, dialect); err != nil || script == "" {
				t.Errorf("migration %d has no %s script: %v", m.Version, dialect, err)
			}
		}
	}
}

func TestMigrateAppliesPendingMigrationsOnce(t *testing.T) {
	t.Parallel()
	sqlstoretest.RunUnmigrated(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		shipped := sqlstore.Migrations()
		if version, err := s.SchemaVersion(ctx); err != nil || version != 0 {
			t.Fatalf("version of an empty database = %d, %v", version, err)
		}
		if pending, err := s.Pending(ctx); err != nil || len(pending) != len(shipped) {
			t.Fatalf("pending on an empty database = %+v, %v", pending, err)
		}
		for range 2 {
			if err := s.Migrate(ctx, state.MigrateOptions{}); err != nil {
				t.Fatalf("migrate: %v", err)
			}
		}
		if version, err := s.SchemaVersion(ctx); err != nil || version != len(shipped) {
			t.Fatalf("version after migrating = %d, %v", version, err)
		}
		if pending, err := s.Pending(ctx); err != nil || len(pending) != 0 {
			t.Fatalf("pending after migrating = %+v, %v", pending, err)
		}
	})
}

func TestIncompatibleMigrationNeedsApprovalAndNothingIsAppliedWithoutIt(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		base := len(sqlstore.Migrations())
		sqlstore.AddReleaseMigrations(s, addNote, dropNote)

		wantCode(t, s.Migrate(ctx, state.MigrateOptions{}), core.CodeApprovalRequired)
		// The compatible migration ahead of the refused one was not applied either.
		if version, err := s.SchemaVersion(ctx); err != nil || version != base {
			t.Fatalf("version after the refusal = %d, %v; want %d", version, err, base)
		}
		pending, err := s.Pending(ctx)
		if err != nil || len(pending) != 2 || len(state.Incompatible(pending)) != 1 {
			t.Fatalf("pending after the refusal = %+v, %v", pending, err)
		}

		if err := s.Migrate(ctx, state.MigrateOptions{AllowIncompatible: true}); err != nil {
			t.Fatalf("an approved incompatible migration must apply: %v", err)
		}
		if version, err := s.SchemaVersion(ctx); err != nil || version != base+2 {
			t.Fatalf("version after approval = %d, %v", version, err)
		}
	})
}

func TestFailedMigrationLeavesTheSchemaWhereItWas(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		base := len(sqlstore.Migrations())
		broken := sqlstore.TestMigration{Name: "change a table that does not exist", BackwardCompatible: true, SQL: `ALTER TABLE missing ADD COLUMN x TEXT`}
		sqlstore.AddReleaseMigrations(s, addNote, broken)
		if err := s.Migrate(ctx, state.MigrateOptions{}); err == nil {
			t.Fatal("a migration that fails must be reported")
		}
		if version, err := s.SchemaVersion(ctx); err != nil || version != base {
			t.Fatalf("version after the failure = %d, %v; want %d", version, err, base)
		}
		// The column of the migration before the broken one was rolled back too.
		if _, err := db.Raw(t).ExecContext(ctx, `SELECT note FROM kv`); err == nil {
			t.Fatal("a failed migration run left part of its changes behind")
		}
	})
}

func TestPreviousReleaseRunsOnACompatibleNewerSchemaOnly(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		newer := db.Open(t, core.ZoneControl)
		sqlstore.AddReleaseMigrations(newer, addNote)
		if err := newer.Migrate(ctx, state.MigrateOptions{}); err != nil {
			t.Fatal(err)
		}
		// After a rollback of the code, the previous release meets a schema one
		// compatible migration ahead of it and keeps working.
		previous := db.Open(t, core.ZoneControl)
		mustTransition(t, previous, received(issue(7)))
		if pending, err := previous.Pending(ctx); err != nil || len(pending) != 0 {
			t.Fatalf("the previous release sees pending migrations: %+v, %v", pending, err)
		}

		// A schema that moved on incompatibly must not be used by code that
		// does not know the change.
		sqlstore.AddReleaseMigrations(newer, dropNote, addHint)
		if err := newer.Migrate(ctx, state.MigrateOptions{AllowIncompatible: true}); err != nil {
			t.Fatal(err)
		}
		stale := db.Open(t, core.ZoneControl)
		wantCode(t, view(stale, func(state.Tx) error { return nil }), core.CodeUnavailable)
		wantCode(t, update(stale, func(tx state.Tx) error {
			_, err := tx.Transition(ctx, received(issue(8)))
			return err
		}), core.CodeUnavailable)
	})
}

func TestOnlyTheControlZoneMigratesTheSchema(t *testing.T) {
	t.Parallel()
	sqlstoretest.RunUnmigrated(t, func(t *testing.T, db *sqlstoretest.Database) {
		for _, zone := range []core.Zone{core.ZoneApplication, core.ZoneAgent} {
			s := db.Open(t, zone)
			wantCode(t, s.Migrate(ctx, state.MigrateOptions{}), core.CodePermissionDenied)
			if version, err := s.SchemaVersion(ctx); err != nil || version != 0 {
				t.Fatalf("version after a refused migration = %d, %v", version, err)
			}
		}
	})
}

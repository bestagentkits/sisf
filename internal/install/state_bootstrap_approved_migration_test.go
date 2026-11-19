package install

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/audit"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// storeBeforeStoreRules prepares a SQLite store as the release before
// migration 12 left it. On SQLite migration 12 changes nothing in the
// schema and the later ones run again unchanged, so a store without their
// bookkeeping rows is that store.
func storeBeforeStoreRules(t *testing.T) BootstrapConfig {
	t.Helper()
	cfg := sqliteConfig(t, nil)
	if err := Bootstrap(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), `DELETE FROM schema_migrations WHERE version >= 12`); err != nil {
		t.Fatal(err)
	}
	if v := schemaVersion(t, cfg); v != 11 {
		t.Fatalf("the store before migration 12 is at %d", v)
	}
	return cfg
}

func schemaVersion(t *testing.T, cfg BootstrapConfig) int {
	t.Helper()
	v, err := openControl(t, cfg).SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func snapshots(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		// Opening a snapshot to look at it adds its SQLite journal files.
		if strings.HasSuffix(e.Name(), ".db") {
			names = append(names, e.Name())
		}
	}
	return names
}

// An existing store takes migration 12 only with the Owner's approval of
// exactly that migration. Without it, or with another set, nothing is
// applied and no snapshot is written; the refusal is one that starting
// again cannot change, so a service manager does not restart into it.
// With it, the store is snapshot first, migrated, and the audit log names
// the approval and the snapshot, which still holds the store as it was.
func TestAnExistingStoreTakesAnIncompatibleMigrationOnlyWithTheOwnersApprovalAndASnapshot(t *testing.T) {
	t.Parallel()
	cfg := storeBeforeStoreRules(t)
	backups := filepath.Join(t.TempDir(), "backups")
	cfg.SnapshotDir = backups

	for name, approved := range map[string][]int{"no approval": nil, "another migration": {13}, "one too many": {12, 13}} {
		cfg.ApproveMigrations = approved
		err := Bootstrap(t.Context(), cfg)
		if !core.HasCode(err, core.CodeApprovalRequired) || !Refused(err) {
			t.Fatalf("%s: %v, want a refusal that needs the Owner's approval", name, err)
		}
		if v := schemaVersion(t, cfg); v != 11 {
			t.Fatalf("%s moved the store to %d", name, v)
		}
		if got := snapshots(t, backups); len(got) != 0 {
			t.Fatalf("%s wrote snapshots %v", name, got)
		}
	}

	cfg.ApproveMigrations = []int{12}
	if err := Bootstrap(t.Context(), cfg); err != nil {
		t.Fatalf("the approved migration: %v", err)
	}
	if v := schemaVersion(t, cfg); v != len(sqlstore.Migrations()) {
		t.Fatalf("the approved store is at %d", v)
	}
	written := snapshots(t, backups)
	if len(written) != 1 {
		t.Fatalf("snapshots after the approval: %v", written)
	}
	snapshot := cfg
	snapshot.DSN = filepath.Join(backups, written[0])
	if v := schemaVersion(t, snapshot); v != 11 {
		t.Fatalf("the snapshot is at %d, want the store as it was", v)
	}

	events, err := audit.New(openControl(t, cfg), cfg.Clock).Events(t.Context(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range events {
		if e.Action == AuditActionApprovedMigration {
			found = e.ActorID == "owner:local" && e.Surface == core.SurfaceCLI && e.Outcome == core.OutcomeExecuted &&
				e.Details["migrations"] == "12" && e.Details["snapshot"] == written[0]
		}
	}
	if !found {
		t.Fatalf("the audit log does not name the approved migration and its snapshot: %+v", events)
	}

	// Run again, nothing is pending: the approval changes nothing and no
	// further snapshot is written.
	if err := Bootstrap(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if got := snapshots(t, backups); len(got) != 1 {
		t.Fatalf("a second run wrote snapshots %v", got)
	}
}

// A snapshot that cannot be written stops the upgrade before anything is
// migrated.
func TestAFailedSnapshotLeavesTheStoreUnmigrated(t *testing.T) {
	t.Parallel()
	cfg := storeBeforeStoreRules(t)
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.SnapshotDir, cfg.ApproveMigrations = filepath.Join(blocker, "backups"), []int{12}
	if err := Bootstrap(t.Context(), cfg); err == nil {
		t.Fatal("an upgrade went on without its snapshot")
	}
	if v := schemaVersion(t, cfg); v != 11 {
		t.Fatalf("a failed snapshot left the store at %d", v)
	}
}

// A PostgreSQL store is dumped by `sisf up` before the state step runs,
// which then names the dump. An approval without a dump is refused before
// anything is migrated.
func TestAnApprovedPostgresMigrationNeedsTheNameOfItsDump(t *testing.T) {
	t.Parallel()
	cfg := BootstrapConfig{FactoryID: "acme-factory", Dialect: sqlstore.DialectPostgres, DSN: "postgres://unused", ApproveMigrations: []int{12}}
	if err := Bootstrap(t.Context(), cfg); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("an approval without a dump: %v", err)
	}
}

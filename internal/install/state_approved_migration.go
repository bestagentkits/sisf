package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/audit"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// AuditActionApprovedMigration is the audit action of a migration that is
// not backward compatible, applied on the Owner's approval.
const AuditActionApprovedMigration = "state.migrate_approved"

// migrateState applies the pending migrations. A migration that is not
// backward compatible runs on an existing store only when the Owner
// approved exactly the incompatible migrations pending, and only after the
// store was snapshot: a SQLite store here, a PostgreSQL store by `sisf up`
// before this step. The applied approval is written to the audit log. A
// refusal for want of approval is one that starting again cannot change.
func migrateState(ctx context.Context, store *sqlstore.Store, cfg BootstrapConfig) error {
	version, err := store.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	pending, err := store.Pending(ctx)
	if err != nil {
		return err
	}
	blocked := state.Incompatible(pending)
	approved := version > 0 && len(blocked) > 0 && state.ApprovesExactly(cfg.ApproveMigrations, blocked)
	snapshot := cfg.Snapshot
	if approved && cfg.Dialect == sqlstore.DialectSQLite {
		snapshot = snapshotName(cfg.ApproveMigrations, cfg.Clock.Now(), ".db")
		if cfg.SnapshotDir == "" {
			return invalidf("an approved migration needs a directory for the snapshot of the store")
		}
		if err := store.SnapshotSQLite(ctx, filepath.Join(cfg.SnapshotDir, snapshot)); err != nil {
			return err
		}
	}
	err = store.Migrate(ctx, state.MigrateOptions{Approved: cfg.ApproveMigrations})
	var refusal *core.Error
	if errors.As(err, &refusal) && refusal.Code == core.CodeApprovalRequired {
		return refusal.WithCause(errRefused)
	}
	if err != nil || !approved {
		return err
	}
	_, err = audit.New(store, cfg.Clock).Append(ctx, core.AuditEvent{
		ActorID: "owner:local", ActorKind: core.ActorHuman, Surface: core.SurfaceCLI,
		Action: AuditActionApprovedMigration, Resource: "state store", Outcome: core.OutcomeExecuted,
		Details: map[string]string{"migrations": joinVersions(cfg.ApproveMigrations), "snapshot": snapshot},
	})
	return err
}

// joinVersions lists versions in ascending order, separated by commas.
func joinVersions(versions []int) string {
	sorted := append([]int(nil), versions...)
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	parts := make([]string, len(sorted))
	for i, v := range sorted {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

// snapshotName names the snapshot of the store taken before the approved
// migrations, at a time, with the extension of its kind.
func snapshotName(approve []int, at time.Time, ext string) string {
	return "state-before-migration-" + joinVersions(approve) + "-" + at.UTC().Format("20060102T150405Z") + ext
}

// writeSnapshotFile writes a snapshot to a new file of dir, readable by its
// owner only, creating dir readable by its owner only.
func writeSnapshotFile(dir, name string, content []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return unavailablef(err, "cannot create %s", dir)
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return unavailablef(err, "cannot create the snapshot %s", path)
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return unavailablef(err, "cannot write the snapshot %s", path)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return unavailablef(err, "cannot write the snapshot %s", path)
	}
	return nil
}

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

var _ state.Migrator = (*Store)(nil)

// migrationLockClass and lockMigrations take the PostgreSQL advisory lock
// two migrators of one schema contend for. The lock is keyed by the schema
// being migrated, so migrators of different schemas in one database, such
// as factories or tests that each own a schema, do not wait for each other.
// coalesce keeps the lock taken when the search path names no schema: the
// lock function ignores a call with a NULL key. SQLite needs no lock: a
// write transaction already excludes the other.
const (
	migrationLockClass int32 = 0x51534d67 // arbitrary, fixed
	lockMigrations           = `SELECT pg_advisory_xact_lock(?, hashtext(coalesce(current_schema(), '')))`
)

const createMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version             BIGINT  NOT NULL PRIMARY KEY,
	name                TEXT    NOT NULL,
	backward_compatible BOOLEAN NOT NULL,
	applied_at          BIGINT  NOT NULL
)`

// SchemaVersion returns the version of the last applied migration, 0 for a
// database without a schema.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	applied, err := s.applied(ctx, &querier{run: s.read, dialect: s.dialect})
	if err != nil || len(applied) == 0 {
		return 0, err
	}
	return applied[len(applied)-1].Version, nil
}

// Pending returns the migrations of this release that are not applied yet.
func (s *Store) Pending(ctx context.Context) ([]state.Migration, error) {
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return nil, err
	}
	var pending []state.Migration
	for _, m := range s.migrations {
		if m.Version > version {
			pending = append(pending, m.Migration)
		}
	}
	return pending, nil
}

// Migrate applies the pending migrations in order, all in one transaction:
// either every one of them is applied or the schema stays where it was. On
// SQLite a migrator that finds the write lock held past its busy timeout
// tries again, for up to migrateBusyWait, and then finds the schema as the
// other migrator left it. Unless the database holds no store yet, a
// pending migration that is not backward compatible needs
// opts.AllowIncompatible, else APPROVAL_REQUIRED and nothing is applied.
// Only a store opened for the control zone migrates.
func (s *Store) Migrate(ctx context.Context, opts state.MigrateOptions) error {
	if s.zone != core.ZoneControl {
		return core.NewError(core.CodePermissionDenied,
			fmt.Sprintf("the %s zone may not migrate the state store", string(s.zone)))
	}
	known := make([]state.Migration, len(s.migrations))
	for i, m := range s.migrations {
		known[i] = m.Migration
	}
	if err := state.ValidateMigrations(known); err != nil {
		return err
	}
	err := s.whileBusy(ctx, func() error { return s.migrate(ctx, opts, known) })
	if err == nil {
		s.schemaChecked.Store(false)
	}
	return err
}

// migrateBusyWait bounds how long a SQLite migrator keeps trying while other
// connections hold the write lock, beyond the busy timeout of each try.
// Migrators of one file start together, such as the units of a Standalone
// install, and the one that loses the race waits for the winner instead of
// failing the start.
const migrateBusyWait = 2 * time.Minute

// migrateBusyPause is the pause between two tries, for a lock that is
// reported busy without waiting out the busy timeout.
const migrateBusyPause = 50 * time.Millisecond

// whileBusy runs try until it succeeds, fails for another reason than a
// SQLite lock held by another connection, ctx ends, or migrateBusyWait has
// passed. The wait is in real time, as the busy timeout of SQLite is.
// PostgreSQL migrators queue on their advisory lock instead.
func (s *Store) whileBusy(ctx context.Context, try func() error) error {
	deadline := time.Now().Add(migrateBusyWait)
	for {
		err := try()
		if err == nil || s.dialect != DialectSQLite || !sqliteBusyError(err) || !time.Now().Before(deadline) {
			return err
		}
		pause := time.NewTimer(migrateBusyPause)
		select {
		case <-ctx.Done():
			pause.Stop()
			return err
		case <-pause.C:
		}
	}
}

// migrate is one try of Migrate: the pending migrations in one transaction.
func (s *Store) migrate(ctx context.Context, opts state.MigrateOptions, known []state.Migration) error {
	return s.inRawTx(ctx, false, func(ctx context.Context, q *querier) error {
		if s.dialect == DialectPostgres {
			if _, err := q.exec(ctx, "lock migrations", lockMigrations, migrationLockClass); err != nil {
				return err
			}
		}
		applied, err := s.applied(ctx, q)
		if err != nil {
			return err
		}
		if len(applied) >= len(s.migrations) {
			return nil
		}
		pending := s.migrations[len(applied):]
		if blocked := state.Incompatible(known[len(applied):]); len(blocked) > 0 && !opts.AllowIncompatible {
			fresh, err := s.fresh(ctx, q, applied)
			if err != nil {
				return err
			}
			if !fresh {
				return approvalRequired(blocked)
			}
		}
		if err := q.script(ctx, "prepare migrations", createMigrationsTable); err != nil {
			return err
		}
		for _, m := range pending {
			script, err := m.script(s.dialect)
			if err != nil {
				return err
			}
			operation := "apply migration " + strconv.Itoa(m.Version)
			if err := q.script(ctx, operation, script); err != nil {
				return err
			}
			if _, err := q.exec(ctx, operation, `INSERT INTO schema_migrations
				(version, name, backward_compatible, applied_at) VALUES (?, ?, ?, ?)`,
				int64(m.Version), m.Name, m.BackwardCompatible, toNanos(s.now())); err != nil {
				return err
			}
		}
		return nil
	})
}

// approvalRequired names the migrations that wait for the Owner.
func approvalRequired(blocked []state.Migration) *core.Error {
	versions := make([]string, len(blocked))
	for i, m := range blocked {
		versions[i] = strconv.Itoa(m.Version)
	}
	refusal := core.NewError(core.CodeApprovalRequired,
		"a pending migration is not backward compatible and needs the Owner's approval, which this release cannot take yet; "+
			"keep the store on the release it runs, or back it up and install this release on a new state store")
	refusal.Details = map[string]string{"migrations": strings.Join(versions, ",")}
	return refusal
}

// fresh reports whether the database holds no store yet: no migration is
// recorded and the table of the lifecycle records does not exist. No release
// runs on such a database and a rollback has no data to keep, so it takes
// every migration of the release, compatible or not. Both facts are the
// owner role's: the zone roles may read the bookkeeping but not write it, and
// may not drop a table.
func (s *Store) fresh(ctx context.Context, q *querier, applied []state.Migration) (bool, error) {
	if len(applied) > 0 {
		return false, nil
	}
	var tables int64
	if err := q.row(ctx, s.dialect.tableExists(), tableRecords).Scan(&tables); err != nil {
		return false, s.dialect.failure("read schema version", err)
	}
	return tables == 0, nil
}

// applied reads the migrations recorded in the database, in order. A
// database without the bookkeeping table has none.
func (s *Store) applied(ctx context.Context, q *querier) ([]state.Migration, error) {
	var tables int64
	if err := q.row(ctx, s.dialect.tableExists(), tableMigrations).Scan(&tables); err != nil {
		return nil, s.dialect.failure("read schema version", err)
	}
	if tables == 0 {
		return nil, nil
	}
	var applied []state.Migration
	err := q.each(ctx, "read schema version",
		`SELECT version, name, backward_compatible FROM schema_migrations ORDER BY version`, nil,
		func(rows *sql.Rows) error {
			var (
				m       state.Migration
				version int64
			)
			if err := rows.Scan(&version, &m.Name, &m.BackwardCompatible); err != nil {
				return err
			}
			m.Version = int(version)
			applied = append(applied, m)
			return nil
		})
	return applied, err
}

// ensureSchema refuses to work on a schema this release cannot use: one
// that is behind it, or one that is ahead by a migration that is not
// backward compatible. A schema ahead by compatible migrations is fine;
// that is the state after a release was rolled back. The answer is cached
// once it is yes.
func (s *Store) ensureSchema(ctx context.Context) error {
	if s.schemaChecked.Load() {
		return nil
	}
	applied, err := s.applied(ctx, &querier{run: s.read, dialect: s.dialect})
	if err != nil {
		return err
	}
	if len(applied) < len(s.migrations) {
		return core.NewError(core.CodeUnavailable, fmt.Sprintf(
			"the state store schema is at version %d and this release needs version %d; migrate it first",
			len(applied), len(s.migrations)))
	}
	for _, m := range applied[len(s.migrations):] {
		if !m.BackwardCompatible {
			return core.NewError(core.CodeUnavailable, fmt.Sprintf(
				"the state store schema has migration %d, which this release does not know and which is not backward compatible",
				m.Version))
		}
	}
	s.schemaChecked.Store(true)
	return nil
}

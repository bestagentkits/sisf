package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

var _ state.Migrator = (*Store)(nil)

// migrationLockKey is the PostgreSQL advisory lock two migrators contend
// for. SQLite needs none: a write transaction already excludes the other.
const migrationLockKey int64 = 0x5153465f6d696772 // arbitrary, fixed

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
// either every one of them is applied or the schema stays where it was. A
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
	err := s.inRawTx(ctx, false, func(ctx context.Context, q *querier) error {
		if s.dialect == DialectPostgres {
			if _, err := q.exec(ctx, "lock migrations", `SELECT pg_advisory_xact_lock(?)`, migrationLockKey); err != nil {
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
			return approvalRequired(blocked)
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
	if err == nil {
		s.schemaChecked.Store(false)
	}
	return err
}

// approvalRequired names the migrations that wait for the Owner.
func approvalRequired(blocked []state.Migration) *core.Error {
	versions := make([]string, len(blocked))
	for i, m := range blocked {
		versions[i] = strconv.Itoa(m.Version)
	}
	refusal := core.NewError(core.CodeApprovalRequired,
		"a pending migration is not backward compatible and needs the Owner's approval")
	refusal.Details = map[string]string{"migrations": strings.Join(versions, ",")}
	return refusal
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

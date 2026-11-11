package sqlstore

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Dialect names the database engine behind a store. The statements the store
// runs are the same for both; the few places where the engines differ are
// the methods of this type.
type Dialect string

const (
	// DialectSQLite is SQLite in WAL mode, the store of the Standalone mode.
	DialectSQLite Dialect = "sqlite"
	// DialectPostgres is PostgreSQL, the store of the Docker mode.
	DialectPostgres Dialect = "postgres"
)

// Valid reports whether d is a supported dialect.
func (d Dialect) Valid() bool { return d == DialectSQLite || d == DialectPostgres }

// Rebind rewrites the `?` placeholders the store writes its statements with
// into the numbered form PostgreSQL expects. No statement of the store
// contains a literal question mark.
func (d Dialect) Rebind(query string) string {
	if d != DialectPostgres {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] != '?' {
			b.WriteByte(query[i])
			continue
		}
		n++
		b.WriteByte('$')
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

// shareLock is appended to a SELECT whose row must not change before the
// transaction ends. SQLite needs nothing: its write transactions take the
// database write lock when they begin.
func (d Dialect) shareLock() string {
	if d == DialectPostgres {
		return " FOR SHARE"
	}
	return ""
}

// tableExists is a query with one parameter, a table name, that counts the
// tables of that name. It works for a role with no privilege on the table.
func (d Dialect) tableExists() string {
	if d == DialectPostgres {
		return `SELECT COUNT(*) FROM pg_catalog.pg_tables WHERE schemaname = current_schema() AND tablename = ?`
	}
	return `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`
}

// SQLite result codes the store reacts to: another connection holds the lock.
const (
	sqliteBusy   = 5
	sqliteLocked = 6
)

// failure turns an error of the database into a typed error. The message
// names the operation only; the driver's text may quote data or statements,
// so it is kept as the cause for operators and never shown to a caller. An
// error that is already typed passes through unchanged.
func (d Dialect) failure(operation string, err error) error {
	if err == nil {
		return nil
	}
	var typed *core.Error
	if errors.As(err, &typed) {
		return err
	}
	code := core.CodeInternal
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code = core.CodeTimeout
	case errors.Is(err, context.Canceled):
		code = core.CodeUnavailable
	case errors.As(err, &pgErr):
		switch {
		case pgErr.Code == "42501": // insufficient_privilege
			code = core.CodePermissionDenied
		case pgErr.Code == "40001", pgErr.Code == "40P01", pgErr.Code == "55P03", strings.HasPrefix(pgErr.Code, "08"):
			// serialization failure, deadlock, lock not available, connection lost
			code = core.CodeUnavailable
		}
	case sqliteBusyError(err):
		code = core.CodeUnavailable
	}
	return core.NewError(code, "state store: "+operation+" failed").WithCause(err)
}

// sqliteBusyError reports whether err comes from a SQLite lock another
// connection holds. A cancelled or expired context is not one, even though
// failure reports both as UNAVAILABLE.
func sqliteBusyError(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	primary := sqliteErr.Code() & 0xff
	return primary == sqliteBusy || primary == sqliteLocked
}

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bestagentkits/sisf/pkg/core"
)

// runner is what a pool and a transaction have in common.
type runner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// querier runs the store's statements in the dialect of the database.
// Statements are written once, with `?` placeholders, and every argument is
// a plain string, int64, bool or byte slice, so both drivers bind it alike.
type querier struct {
	run     runner
	dialect Dialect
}

// exec runs a statement and returns how many rows it changed.
func (q *querier) exec(ctx context.Context, operation, query string, args ...any) (int64, error) {
	result, err := q.run.ExecContext(ctx, q.dialect.Rebind(query), args...)
	if err != nil {
		return 0, q.dialect.failure(operation, err)
	}
	changed, err := result.RowsAffected()
	return changed, q.dialect.failure(operation, err)
}

// script runs a migration script as it is written: several statements, in
// the dialect of the database, with no placeholders to rewrite.
func (q *querier) script(ctx context.Context, operation, statements string) error {
	_, err := q.run.ExecContext(ctx, statements)
	return q.dialect.failure(operation, err)
}

// row runs a query that yields at most one row.
func (q *querier) row(ctx context.Context, query string, args ...any) *sql.Row {
	return q.run.QueryRowContext(ctx, q.dialect.Rebind(query), args...)
}

// each runs a query and calls scan once per row.
func (q *querier) each(ctx context.Context, operation, query string, args []any, scan func(rows *sql.Rows) error) error {
	rows, err := q.run.QueryContext(ctx, q.dialect.Rebind(query), args...)
	if err != nil {
		return q.dialect.failure(operation, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return q.dialect.failure(operation, err)
		}
	}
	return q.dialect.failure(operation, rows.Err())
}

// limited appends a LIMIT clause when limit is positive. Zero and negative
// limits mean no limit, like the zero fields of a filter.
func limited(query string, args []any, limit int) (string, []any) {
	if limit <= 0 {
		return query, args
	}
	return query + " LIMIT ?", append(args, int64(limit))
}

// Instants are stored as nanoseconds since the Unix epoch in UTC, which is
// exact and identical on both engines. Zero stands for an unset instant.
func toNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func fromNanos(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

// timeOr returns t in UTC, or fallback when t is unset.
func timeOr(t, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t.UTC().Round(0)
}

// bytesOrEmpty keeps a nil payload out of NOT NULL columns.
func bytesOrEmpty(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// requireText checks a value used as an identifier: it must be present and
// storable as text on both engines.
func requireText(name, value string) error {
	if value == "" {
		return invalid("%s must not be empty", name)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return invalid("%s must be valid text", name)
	}
	return nil
}

// cleanText makes free text storable on both engines without refusing it.
func cleanText(value string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(value, "�"), "\x00", "")
}

func invalid(format string, args ...any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, fmt.Sprintf(format, args...))
}

func notFound(format string, args ...any) *core.Error {
	return core.NewError(core.CodeNotFound, fmt.Sprintf(format, args...))
}

func conflict(format string, args ...any) *core.Error {
	return core.NewError(core.CodeConflict, fmt.Sprintf(format, args...))
}

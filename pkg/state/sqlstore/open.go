package sqlstore

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	// The SQLite driver, registered as "sqlite".
	_ "modernc.org/sqlite"
)

const (
	// sqliteBusyTimeout is how long a connection waits for a lock held by
	// another connection or process before it gives up with UNAVAILABLE.
	sqliteBusyTimeout = "5000"
	sqliteReaders     = 4
	postgresMaxConns  = 10
)

// openSQLite opens the database file in WAL mode with two pools. The write
// pool has one connection and begins every transaction with the write lock
// taken, so writers queue instead of failing halfway; several units of a
// Standalone install may share the file, and the busy timeout covers them.
// The read pool cannot write at all.
func openSQLite(path string) (write, read *sql.DB, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, nil, err
	}
	// The file holds the ledger and the audit log. Create it for the owner
	// only before SQLite would create it with the default mode; the WAL
	// files take their mode from it.
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if err := file.Close(); err != nil {
		return nil, nil, err
	}

	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	base := "file:" + (&url.URL{Path: uriPath}).EscapedPath() +
		"?_pragma=busy_timeout(" + sqliteBusyTimeout + ")&_pragma=journal_mode(WAL)"

	if write, err = sql.Open("sqlite", base+"&_txlock=immediate"); err != nil {
		return nil, nil, err
	}
	write.SetMaxOpenConns(1)
	if read, err = sql.Open("sqlite", base+"&_pragma=query_only(1)"); err != nil {
		_ = write.Close()
		return nil, nil, err
	}
	read.SetMaxOpenConns(sqliteReaders)
	return write, read, nil
}

// openPostgres opens a bounded connection pool. The role in the connection
// string decides what the database itself allows the zone to do.
func openPostgres(dsn string) (*sql.DB, error) {
	config, err := postgresConfig(dsn)
	if err != nil {
		return nil, err
	}
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(postgresMaxConns)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return db, nil
}

// postgresConfig parses the connection string and makes sure the server,
// not the driver, binds the arguments of every statement.
//
// In the simple protocol the driver pastes the arguments into the statement
// text itself, which is the one place where an argument could be read as
// SQL. A connection string that asks for it gets the mode that still works
// through a connection pooler but sends arguments apart from the statement.
func postgresConfig(dsn string) (*pgx.ConnConfig, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if config.DefaultQueryExecMode == pgx.QueryExecModeSimpleProtocol {
		config.DefaultQueryExecMode = pgx.QueryExecModeExec
	}
	return config, nil
}

// Package sqlstore is the SQL implementation of the operational store: one
// set of statements run on SQLite in WAL mode (Standalone) or on PostgreSQL
// (Docker).
//
// Every write happens in a transaction. A state change is a compare-and-swap
// on the record version, a lease holder is ordered by its fencing token, a
// delivery is stored once, side effects wait in an outbox, and the budget
// ledger commits with the claim it pays for. A store is opened for one
// factory and one zone: it never reads or writes the rows of another
// factory, and it refuses the operations its zone was not granted.
//
// The audit log shares the database. Its table accepts inserts only, and
// this package offers no way to change or remove a row of it.
package sqlstore

import (
	"context"
	"database/sql"
	"sync/atomic"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Config says which database to open and on whose behalf.
type Config struct {
	Dialect Dialect
	// DSN is the path of the database file for SQLite and a connection
	// string for PostgreSQL. It may carry a credential and is never logged.
	DSN string
	// FactoryID is the factory every row read or written belongs to.
	FactoryID core.FactoryID
	// Zone is the zone the caller runs in; it decides what the store allows.
	Zone core.Zone
	// Clock stamps writes and decides lease expiry. Nil means the wall clock.
	Clock core.Clock
}

// Store is the operational store of one factory, opened for one zone. It is
// safe for concurrent use.
type Store struct {
	dialect Dialect
	factory core.FactoryID
	zone    core.Zone
	clock   core.Clock
	// write runs read-write transactions and read runs read-only ones. They
	// are separate pools on SQLite, where one connection writes at a time,
	// and the same pool on PostgreSQL.
	write, read *sql.DB
	// migrations are the migrations this release knows, in order.
	migrations []migration
	// schemaChecked is set once the schema was found fit for this release.
	schemaChecked atomic.Bool
}

// Open connects to the database. It does not create or change the schema:
// until Migrate has run, every transaction is refused with UNAVAILABLE.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if !cfg.Dialect.Valid() {
		return nil, invalid("store dialect %q must be %s or %s", string(cfg.Dialect), DialectSQLite, DialectPostgres)
	}
	if cfg.DSN == "" {
		return nil, invalid("the store needs a database to open")
	}
	if err := cfg.FactoryID.Validate(); err != nil {
		return nil, err
	}
	if cfg.Zone.Number() == 0 {
		return nil, invalid("the store is opened for one of the three zones, not %q", string(cfg.Zone))
	}
	s := &Store{
		dialect: cfg.Dialect, factory: cfg.FactoryID, zone: cfg.Zone,
		clock: cfg.Clock, migrations: releaseMigrations,
	}
	if s.clock == nil {
		s.clock = core.SystemClock{}
	}
	var err error
	if cfg.Dialect == DialectSQLite {
		s.write, s.read, err = openSQLite(cfg.DSN)
	} else {
		s.write, err = openPostgres(cfg.DSN)
		s.read = s.write
	}
	if err != nil {
		return nil, s.dialect.failure("open", err)
	}
	for _, db := range []*sql.DB{s.write, s.read} {
		if err := db.PingContext(ctx); err != nil {
			_ = s.Close()
			return nil, s.dialect.failure("connect", err)
		}
	}
	return s, nil
}

// FactoryID returns the factory the store belongs to.
func (s *Store) FactoryID() core.FactoryID { return s.factory }

// Zone returns the zone the store was opened for.
func (s *Store) Zone() core.Zone { return s.zone }

// Dialect returns the database engine behind the store.
func (s *Store) Dialect() Dialect { return s.dialect }

// Close releases the database connections.
func (s *Store) Close() error {
	err := s.write.Close()
	if s.read != s.write {
		if readErr := s.read.Close(); err == nil {
			err = readErr
		}
	}
	return s.dialect.failure("close", err)
}

// Update runs fn in one read-write transaction. It commits only when fn
// returns nil; any error, or a panic, rolls back every write fn made, and
// the error of fn comes back unchanged. fn works through the transaction it
// is given: starting another transaction of the same store from the context
// it receives, which includes appending to the audit log, is refused.
func (s *Store) Update(ctx context.Context, fn func(ctx context.Context, tx state.Tx) error) error {
	return s.transact(ctx, false, fn)
}

// View runs fn in one read-only transaction that sees a single snapshot of
// the store. Every write attempted through it is refused.
func (s *Store) View(ctx context.Context, fn func(ctx context.Context, tx state.Tx) error) error {
	return s.transact(ctx, true, fn)
}

func (s *Store) transact(ctx context.Context, readOnly bool, fn func(ctx context.Context, tx state.Tx) error) error {
	if fn == nil {
		return invalid("a transaction needs a function to run")
	}
	return s.inTx(ctx, readOnly, func(ctx context.Context, q *querier) error {
		return fn(ctx, &tx{store: s, q: q, readOnly: readOnly})
	})
}

// openTx is the context key that marks a call as running inside a
// transaction of one store.
type openTx struct{ store *Store }

// inTx runs fn inside one database transaction after making sure the schema
// fits this release.
func (s *Store) inTx(ctx context.Context, readOnly bool, fn func(ctx context.Context, q *querier) error) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	return s.inRawTx(ctx, readOnly, fn)
}

// inRawTx is inTx without the schema check; migrations run through it.
//
// The context handed to fn is marked, and a second transaction started from
// it is refused. On SQLite it would wait forever for the writer the first
// one holds; on PostgreSQL it would silently not be part of the first. The
// refusal makes both engines behave alike.
func (s *Store) inRawTx(ctx context.Context, readOnly bool, fn func(ctx context.Context, q *querier) error) error {
	if ctx.Value(openTx{s}) != nil {
		return invalid("a transaction of this store is already open in this call; finish it before starting another")
	}
	db, opts := s.write, (*sql.TxOptions)(nil)
	if readOnly {
		db = s.read
		if s.dialect == DialectPostgres {
			// Repeatable read gives the whole transaction one snapshot, as a
			// SQLite read transaction has by nature.
			opts = &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead}
		}
	}
	sqlTx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return s.dialect.failure("begin transaction", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = sqlTx.Rollback()
		}
	}()
	if err := fn(context.WithValue(ctx, openTx{s}, true), &querier{run: sqlTx, dialect: s.dialect}); err != nil {
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return s.dialect.failure("commit", err)
	}
	committed = true
	return nil
}

// now is the instant a write is stamped with: UTC, without the monotonic
// reading, so that it equals itself after a round trip through the database.
func (s *Store) now() time.Time { return s.clock.Now().UTC().Round(0) }

// tx is one transaction of a store. It implements state.Tx.
type tx struct {
	store    *Store
	q        *querier
	readOnly bool
}

var _ state.Tx = (*tx)(nil)

package sqlstore_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// The store must satisfy the frozen contracts as they are written.
var (
	_ state.Store    = (*sqlstore.Store)(nil)
	_ state.Migrator = (*sqlstore.Store)(nil)
)

func TestOpenRefusesAnIncompleteConfiguration(t *testing.T) {
	t.Parallel()
	valid := sqlstore.Config{
		Dialect: sqlstore.DialectSQLite, DSN: filepath.Join(t.TempDir(), "state.db"),
		FactoryID: sqlstoretest.Factory, Zone: core.ZoneControl,
	}
	for name, change := range map[string]func(*sqlstore.Config){
		"unknown dialect": func(c *sqlstore.Config) { c.Dialect = "mysql" },
		"no dialect":      func(c *sqlstore.Config) { c.Dialect = "" },
		"no database":     func(c *sqlstore.Config) { c.DSN = "" },
		"no factory":      func(c *sqlstore.Config) { c.FactoryID = "" },
		"bad factory":     func(c *sqlstore.Config) { c.FactoryID = "Not A Factory" },
		"no zone":         func(c *sqlstore.Config) { c.Zone = "" },
		"shared is no zone": func(c *sqlstore.Config) {
			c.Zone = core.ZoneShared
		},
	} {
		cfg := valid
		change(&cfg)
		if s, err := sqlstore.Open(ctx, cfg); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
			if s != nil {
				_ = s.Close()
			}
		}
	}
	s, err := sqlstore.Open(ctx, valid)
	if err != nil {
		t.Fatalf("a complete configuration must open: %v", err)
	}
	if s.FactoryID() != sqlstoretest.Factory || s.Zone() != core.ZoneControl || s.Dialect() != sqlstore.DialectSQLite {
		t.Fatalf("store identity = %s, %s, %s", s.FactoryID(), s.Zone(), s.Dialect())
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestSQLiteDatabaseFileIsReadableByItsOwnerOnly(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "state.db")
	s, err := sqlstore.Open(ctx, sqlstore.Config{
		Dialect: sqlstore.DialectSQLite, DSN: path, FactoryID: sqlstoretest.Factory, Zone: core.ZoneControl,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx, state.MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	mustTransition(t, s, received(issue(7)))
	files, err := filepath.Glob(path + "*")
	if err != nil || len(files) == 0 {
		t.Fatalf("database files = %v, %v", files, err)
	}
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has mode %v; the ledger and audit log must not be readable by others", filepath.Base(file), info.Mode().Perm())
		}
	}
}

func TestReadOnlyTransactionRefusesEveryWrite(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		lease, err := acquire(s, "dispatcher", "dispatcher-1", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		for name, write := range map[string]func(tx state.Tx) error{
			"transition": func(tx state.Tx) error { _, err := tx.Transition(ctx, received(issue(7))); return err },
			"acquire":    func(tx state.Tx) error { _, err := tx.AcquireLease(ctx, "x", "o", time.Minute); return err },
			"renew":      func(tx state.Tx) error { _, err := tx.RenewLease(ctx, lease, time.Minute); return err },
			"release":    func(tx state.Tx) error { return tx.ReleaseLease(ctx, lease) },
			"accept": func(tx state.Tx) error {
				_, err := tx.AcceptDelivery(ctx, state.InboxEntry{Source: "github", DeliveryID: "d-1"})
				return err
			},
			"processed":   func(tx state.Tx) error { return tx.MarkDeliveryProcessed(ctx, "github", "d-1") },
			"enqueue":     func(tx state.Tx) error { return tx.Enqueue(ctx, state.OutboxMessage{Topic: "t", Key: "k"}) },
			"sent":        func(tx state.Tx) error { return tx.MarkOutboxSent(ctx, 1) },
			"failed":      func(tx state.Tx) error { return tx.MarkOutboxFailed(ctx, 1, time.Time{}, "x") },
			"reservation": func(tx state.Tx) error { return tx.PutReservation(ctx, reservation("r-1", 1)) },
			"usage":       func(tx state.Tx) error { return tx.AppendUsage(ctx, spend(1, false, time.Time{})) },
			"put":         func(tx state.Tx) error { _, err := tx.PutKV(ctx, state.KVEntry{Namespace: "n", Key: "k"}); return err },
			"delete":      func(tx state.Tx) error { return tx.DeleteKV(ctx, "n", "k", 1) },
		} {
			if err := view(s, write); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("%s in a read-only transaction: want %s, got %v", name, core.CodeInvalidArgument, err)
			}
		}
		if err := fencing(s, "dispatcher", lease.Token); err != nil {
			t.Fatalf("the lease must be untouched: %v", err)
		}
	})
}

func TestFactoriesSharingADatabaseDoNotSeeEachOther(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ours := db.Open(t, core.ZoneControl)
		theirs := db.OpenFactory(t, "another-factory", core.ZoneControl)

		mustTransition(t, ours, received(issue(7)))
		if _, err := acquire(ours, "dispatcher", "dispatcher-1", time.Hour); err != nil {
			t.Fatal(err)
		}
		if ok, err := accept(ours, state.InboxEntry{Source: "github", DeliveryID: "d-1"}); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if _, err := put(ours, state.KVEntry{Namespace: "policy", Key: "revision", Value: []byte("ours")}); err != nil {
			t.Fatal(err)
		}
		mustUpdate(t, ours, func(tx state.Tx) error { return tx.PutReservation(ctx, reservation("r-1", 5)) })

		// The other factory starts from nothing on the same issue, lease and keys.
		wantCode(t, view(theirs, func(tx state.Tx) error {
			_, err := tx.GetRecord(ctx, issue(7))
			return err
		}), core.CodeNotFound)
		created := mustTransition(t, theirs, received(issue(7)))
		if created.FactoryID != "another-factory" || created.Version != 1 {
			t.Fatalf("their record = %+v", created)
		}
		lease, err := acquire(theirs, "dispatcher", "dispatcher-9", time.Hour)
		if err != nil || lease.Token != 1 {
			t.Fatalf("their lease = %+v, %v", lease, err)
		}
		if ok, err := accept(theirs, state.InboxEntry{Source: "github", DeliveryID: "d-1"}); err != nil || !ok {
			t.Fatalf("their delivery = %v, %v", ok, err)
		}
		if got := keys(t, theirs, "policy", "", 0); len(got) != 0 {
			t.Fatalf("their documents = %v", got)
		}
		if got := totals(t, theirs, time.Time{}); got.Reserved != 0 {
			t.Fatalf("their reserved amount = %+v", got)
		}
		if len(pendingDeliveries(t, ours, 0)) != 1 {
			t.Fatal("our inbox must hold only our delivery")
		}
	})
}

func TestStoreWithoutASchemaIsUnavailableUntilMigrated(t *testing.T) {
	t.Parallel()
	sqlstoretest.RunUnmigrated(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		wantCode(t, view(s, func(state.Tx) error { return nil }), core.CodeUnavailable)
		wantCode(t, update(s, func(state.Tx) error { return nil }), core.CodeUnavailable)
		if err := s.Migrate(ctx, state.MigrateOptions{}); err != nil {
			t.Fatal(err)
		}
		mustTransition(t, s, received(issue(7)))
	})
}

func TestTransactionStartedInsideAnotherIsRefusedInsteadOfHanging(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		s := db.Open(t, core.ZoneControl)
		nothing := func(context.Context, state.Tx) error { return nil }
		err := s.Update(ctx, func(inner context.Context, tx state.Tx) error {
			wantCode(t, s.Update(inner, nothing), core.CodeInvalidArgument)
			wantCode(t, s.View(inner, nothing), core.CodeInvalidArgument)
			_, err := s.AppendAuditRow(inner, func(int64, string) (string, []byte, error) { return "h", nil, nil })
			wantCode(t, err, core.CodeInvalidArgument)
			wantCode(t, s.Migrate(inner, state.MigrateOptions{}), core.CodeInvalidArgument)
			// The transaction that is open goes on and commits.
			_, err = tx.Transition(inner, received(issue(7)))
			return err
		})
		if err != nil {
			t.Fatalf("the outer transaction must commit: %v", err)
		}
		if got := record(t, s, issue(7)); got.Version != 1 {
			t.Fatalf("record written by the outer transaction = %+v", got)
		}
		// Once it has ended, the same context starts a transaction again.
		if err := s.View(ctx, nothing); err != nil {
			t.Fatal(err)
		}
	})
}

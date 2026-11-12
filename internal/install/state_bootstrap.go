package install

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	// The PostgreSQL driver the role statements run on, registered as "pgx".
	_ "github.com/jackc/pgx/v5/stdlib"

	budgetgate "github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// BootstrapConfig is the install-time preparation of the state store.
type BootstrapConfig struct {
	FactoryID core.FactoryID
	Dialect   sqlstore.Dialect
	// DSN opens the store as its owner: a database file for SQLite, a
	// connection string of the owning role for PostgreSQL. The owner is used
	// here and nowhere else; no zone is given it.
	DSN string
	// Roles and Passwords give each zone its own PostgreSQL role. They are
	// ignored for SQLite, which has no roles.
	Roles     map[core.Zone]string
	Passwords map[core.Zone]string
	// Budget is the Owner's confirmation, stored where the dispatcher's
	// budget gate looks for it; nil stores nothing, and the gate then refuses
	// every claim.
	Budget *BudgetConfirmation
	Clock  core.Clock
}

// Bootstrap migrates the schema with a control-zone store, creates one role
// per zone and grants each exactly what its zone may reach, then records the
// budget confirmation. It is safe to run again: migrations apply once, roles
// are updated in place (which also rotates a password), grants are re-issued
// from scratch and the confirmation is replaced.
func Bootstrap(ctx context.Context, cfg BootstrapConfig) error {
	store, err := sqlstore.Open(ctx, sqlstore.Config{
		Dialect: cfg.Dialect, DSN: cfg.DSN, FactoryID: cfg.FactoryID, Zone: core.ZoneControl, Clock: cfg.Clock,
	})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }() // the work is committed or has failed by now
	if err := store.Migrate(ctx, state.MigrateOptions{}); err != nil {
		return err
	}
	if cfg.Dialect == sqlstore.DialectPostgres {
		if err := ensureRoles(ctx, cfg); err != nil {
			return err
		}
		if err := store.GrantZoneRoles(ctx, sqlstore.ZoneRoles(cfg.Roles)); err != nil {
			return err
		}
	}
	if cfg.Budget == nil {
		return nil
	}
	if err := storeBudgetConfirmation(ctx, store, *cfg.Budget); err != nil {
		return err
	}
	return storeCappedSettings(ctx, store, *cfg.Budget)
}

// storeCappedSettings records the cap of a capped confirmation as the
// budget settings in force, which the gate needs before it admits a run
// under a cap. Settings that exist are the Owner's later decision and are
// never replaced here.
func storeCappedSettings(ctx context.Context, store *sqlstore.Store, rec BudgetConfirmation) error {
	if rec.Mode != core.BudgetCapped {
		return nil
	}
	settings := budgetgate.Settings{Mode: core.BudgetCapped, CapMicros: rec.CapMicros, AlertThresholdMicros: rec.AlertThresholdMicros}
	if err := settings.Validate(); err != nil {
		return err
	}
	value, err := json.Marshal(settings)
	if err != nil {
		return invalidf("the budget settings cannot be encoded")
	}
	return store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if _, err := tx.GetKV(ctx, budgetgate.SettingsNamespace, budgetgate.SettingsKey); err == nil {
			return nil
		} else if !isNotFound(err) {
			return err
		}
		_, err := tx.PutKV(ctx, state.KVEntry{Namespace: budgetgate.SettingsNamespace, Key: budgetgate.SettingsKey, Value: value})
		return err
	})
}

// ensureRoles creates each zone's login role, or sets the password of one
// that exists. Roles can neither create databases or roles nor be
// superusers, and none passes the row policies the grants write. Statements
// are formatted by the server, which quotes names and literals, so no value
// is ever spliced into SQL here.
func ensureRoles(ctx context.Context, cfg BootstrapConfig) error {
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return unavailablef(err, "cannot open the state database")
	}
	defer func() { _ = db.Close() }()
	for _, zone := range core.Zones() {
		role, password := cfg.Roles[zone], cfg.Passwords[zone]
		if role == "" || password == "" {
			return invalidf("the %s zone needs a role name and a password", zone)
		}
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
			return unavailablef(redactDriverError(err), "cannot look up the %s zone's role", zone)
		}
		verb := "CREATE"
		if exists {
			verb = "ALTER"
		}
		var statement string
		query := `SELECT format('` + verb + ` ROLE %I WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L', $1::text, $2::text)`
		if err := db.QueryRowContext(ctx, query, role, password).Scan(&statement); err != nil {
			return unavailablef(redactDriverError(err), "cannot prepare the %s zone's role", zone)
		}
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return unavailablef(redactDriverError(err), "cannot %s the %s zone's role", verb, zone)
		}
	}
	return nil
}

// redactDriverError keeps a driver error from carrying a connection string
// or a password into a message: a server error keeps its code and text, which
// name no value of ours, and any other error is replaced by a fixed text.
func redactDriverError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return fmt.Errorf("database error %s: %s", pg.Code, pg.Message)
	}
	return errors.New("the database could not be reached or refused the statement")
}

// storeBudgetConfirmation writes the confirmation, replacing an earlier one.
func storeBudgetConfirmation(ctx context.Context, store *sqlstore.Store, rec BudgetConfirmation) error {
	value, err := json.Marshal(rec)
	if err != nil {
		return invalidf("the budget confirmation cannot be encoded")
	}
	return store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		entry := state.KVEntry{Namespace: BudgetNamespace, Key: BudgetKey, Value: value}
		if current, err := tx.GetKV(ctx, BudgetNamespace, BudgetKey); err == nil {
			entry.Version = current.Version
		} else if !isNotFound(err) {
			return err
		}
		_, err := tx.PutKV(ctx, entry)
		return err
	})
}

// ReadBudgetConfirmation loads the confirmation from a store; ok is false
// when the Owner has not confirmed.
func ReadBudgetConfirmation(ctx context.Context, view state.Store) (rec BudgetConfirmation, ok bool, err error) {
	err = view.View(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, BudgetNamespace, BudgetKey)
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(entry.Value, &rec); err != nil {
			return invalidf("the stored budget confirmation is unreadable")
		}
		ok = true
		return nil
	})
	return rec, ok, err
}

func isNotFound(err error) bool {
	var typed *core.Error
	return errors.As(err, &typed) && typed.Code == core.CodeNotFound
}

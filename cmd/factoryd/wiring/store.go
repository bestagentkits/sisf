package wiring

import (
	"context"
	"strings"

	"github.com/bestagentkits/sisf/internal/manifestsettings"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// DialectOf tells the store engine from a connection string: a PostgreSQL
// URL, or the path of a SQLite file.
func DialectOf(dsn string) sqlstore.Dialect {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return sqlstore.DialectPostgres
	}
	return sqlstore.DialectSQLite
}

// StateStoreDir is the directory of a SQLite state store's file, absolute,
// and empty for a server DSN; see manifestsettings.StateStoreDir.
func StateStoreDir(dsn string) string { return manifestsettings.StateStoreDir(dsn) }

// OpenStore opens the operational store for one zone. A store the
// installer has not prepared yet is refused: the units do not migrate on
// their own, except that the control zone applies migrations that are
// backward compatible, so a Standalone upgrade needs no extra step.
func OpenStore(ctx context.Context, dsn core.SecretValue, factory core.FactoryID, zone core.Zone, clock core.Clock) (*sqlstore.Store, error) {
	raw := string(dsn.Reveal())
	store, err := sqlstore.Open(ctx, sqlstore.Config{Dialect: DialectOf(raw), DSN: raw, FactoryID: factory, Zone: zone, Clock: clock})
	if err != nil {
		return nil, err
	}
	pending, err := store.Pending(ctx)
	switch {
	case err != nil:
		_ = store.Close()
		return nil, err
	case len(pending) > 0 && zone != core.ZoneControl:
		_ = store.Close()
		return nil, core.NewError(core.CodeUnavailable, "the state store is not prepared yet; run `sisf up`, which migrates it")
	case len(pending) > 0:
		if err := store.Migrate(ctx, state.MigrateOptions{}); err != nil {
			_ = store.Close()
			return nil, err
		}
	}
	return store, nil
}

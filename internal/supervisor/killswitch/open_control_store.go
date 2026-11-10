package killswitch

import (
	"context"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// EnvStateDSN is the environment variable the supervisor and the CLI read the
// control zone's store connection from by default. It may hold a credential
// and is never logged or passed on a command line.
const EnvStateDSN = "SISF_STATE_DSN"

// OpenControlStore opens the operational store of factory as the control
// zone, from a PostgreSQL URL or the path of a SQLite file. It never
// migrates: a stop must not wait on, or change, the schema, so a store that
// is not prepared is UNAVAILABLE.
func OpenControlStore(ctx context.Context, dsn core.SecretValue, factory core.FactoryID, clock core.Clock) (*sqlstore.Store, error) {
	raw := strings.TrimSpace(string(dsn.Reveal()))
	if raw == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "no state store connection was given; set "+EnvStateDSN)
	}
	dialect := sqlstore.DialectSQLite
	if strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") {
		dialect = sqlstore.DialectPostgres
	}
	store, err := sqlstore.Open(ctx, sqlstore.Config{Dialect: dialect, DSN: raw, FactoryID: factory, Zone: core.ZoneControl, Clock: clock})
	if err != nil {
		return nil, err
	}
	pending, err := store.Pending(ctx)
	if err == nil && len(pending) > 0 {
		err = core.NewError(core.CodeUnavailable, "the state store is not prepared; run `sisf up`, which migrates it")
	}
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

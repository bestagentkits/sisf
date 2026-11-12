package install

import (
	"context"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// Environment variables the one-shot state step of Docker mode reads. They
// come from an env file only that step is given, never from the command line.
const (
	EnvStateOwnerDSN = "SISF_STATE_OWNER_DSN"
	envZonePassword  = "SISF_ZONE_PASSWORD_"
)

// BootstrapEnv is the command-line form of Bootstrap: it names the database
// and takes every credential from the environment.
type BootstrapEnv struct {
	FactoryID core.FactoryID
	Dialect   sqlstore.Dialect
	// SQLitePath is the database file for the SQLite dialect.
	SQLitePath string
	// Budget is stored as the Owner's confirmation when it is confirmed.
	Budget BudgetChoice
	Getenv func(string) string
	Clock  core.Clock
}

// RunBootstrap prepares the state store from the environment: the owner's
// connection string and one password per zone for PostgreSQL, the file path
// for SQLite. A missing value is a refusal naming the variable, never a
// default, so a role can not be created with an empty or shared password.
func RunBootstrap(ctx context.Context, e BootstrapEnv) error {
	if err := e.FactoryID.Validate(); err != nil {
		return err
	}
	if err := e.Budget.Validate(); err != nil {
		return err
	}
	cfg := BootstrapConfig{FactoryID: e.FactoryID, Dialect: e.Dialect, Clock: e.Clock}
	if rec, ok := e.Budget.confirmation(e.Clock.Now()); ok {
		cfg.Budget = &rec
	}
	switch e.Dialect {
	case sqlstore.DialectSQLite:
		if e.SQLitePath == "" {
			return invalidf("the SQLite store needs a database path")
		}
		cfg.DSN = e.SQLitePath
	case sqlstore.DialectPostgres:
		cfg.DSN = e.Getenv(EnvStateOwnerDSN)
		if cfg.DSN == "" {
			return invalidf("%s is not set", EnvStateOwnerDSN)
		}
		cfg.Roles, cfg.Passwords = zoneRoles, map[core.Zone]string{}
		seen := map[string]core.Zone{}
		for _, z := range core.Zones() {
			name := envZonePassword + strings.ToUpper(string(z))
			pw := e.Getenv(name)
			if pw == "" {
				return invalidf("%s is not set", name)
			}
			if other, dup := seen[pw]; dup {
				return invalidf("the %s and %s zones were given the same password", other, z)
			}
			seen[pw] = z
			cfg.Passwords[z] = pw
		}
	default:
		return invalidf("the dialect must be %s or %s", sqlstore.DialectSQLite, sqlstore.DialectPostgres)
	}
	return Bootstrap(ctx, cfg)
}

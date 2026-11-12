package main

import (
	"context"
	"os"

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

func init() {
	register(Command{
		Path:    []string{"state", "bootstrap"},
		Args:    "--dialect postgres|sqlite --factory-id id [--sqlite-path file] [--confirm-unlimited (--alert-micros n | --no-alert)]",
		Summary: "Prepare the state store: migrate, create one role per zone, record the budget",
		Help: `
Run once by "sisf up" in Docker mode, inside the Compose project, as the
store's owner. Migrates the schema, creates a database role for each of the
three zones, grants each only what its zone may reach, and records the
Owner's budget confirmation where the dispatcher's budget gate looks for it.
Safe to run again.

PostgreSQL credentials are read from the environment, never the command line:
  ` + install.EnvStateOwnerDSN + `
  SISF_ZONE_PASSWORD_APPLICATION, SISF_ZONE_PASSWORD_AGENT, SISF_ZONE_PASSWORD_CONTROL`,
		Run: runStateBootstrap,
	})
}

func runStateBootstrap(ctx context.Context, env *Env, args []string) error {
	var e install.BootstrapEnv
	var dialect, factoryID string
	var confirm, noAlert bool
	var alertMicros int64
	fs := newFlagSet("state bootstrap")
	fs.StringVar(&dialect, "dialect", "", "postgres or sqlite")
	fs.StringVar(&factoryID, "factory-id", "", "factory id of the store")
	fs.StringVar(&e.SQLitePath, "sqlite-path", "", "database file (sqlite)")
	fs.BoolVar(&confirm, "confirm-unlimited", false, "record the unlimited-budget confirmation")
	fs.Int64Var(&alertMicros, "alert-micros", 0, "alert threshold in micros (with --confirm-unlimited)")
	fs.BoolVar(&noAlert, "no-alert", false, "no budget alert (with --confirm-unlimited)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	e.Dialect, e.FactoryID = sqlstore.Dialect(dialect), core.FactoryID(factoryID)
	if !e.Dialect.Valid() {
		return usagef("--dialect must be postgres or sqlite")
	}
	if factoryID == "" {
		return usagef("--factory-id is required")
	}
	if !confirm && (noAlert || alertMicros != 0) {
		return usagef("--alert-micros and --no-alert need --confirm-unlimited")
	}
	e.Budget = install.BudgetChoice{Confirmed: confirm, AlertOff: noAlert, AlertThresholdMicros: core.Micros(alertMicros)}
	if err := e.Budget.Validate(); err != nil {
		return usagef("%v", err)
	}
	e.Getenv, e.Clock = os.Getenv, core.SystemClock{}
	if err := install.RunBootstrap(ctx, e); err != nil {
		return err
	}
	return env.Printf("state store of %s is ready\n", factoryID)
}

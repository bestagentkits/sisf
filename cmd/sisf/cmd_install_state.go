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
		Args:    "--dialect postgres|sqlite --factory-id id [--sqlite-path file] [--confirm-unlimited (--alert-micros n | --no-alert) | --cap-micros n --run-estimate-micros n [--alert-micros n]]",
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
	var alertMicros, capMicros, estimateMicros int64
	fs := newFlagSet("state bootstrap")
	fs.StringVar(&dialect, "dialect", "", "postgres or sqlite")
	fs.StringVar(&factoryID, "factory-id", "", "factory id of the store")
	fs.StringVar(&e.SQLitePath, "sqlite-path", "", "database file (sqlite)")
	fs.BoolVar(&confirm, "confirm-unlimited", false, "record the unlimited-budget confirmation")
	fs.Int64Var(&alertMicros, "alert-micros", 0, "alert threshold in micros (with --confirm-unlimited)")
	fs.BoolVar(&noAlert, "no-alert", false, "no budget alert (with --confirm-unlimited)")
	fs.Int64Var(&capMicros, "cap-micros", 0, "record a capped budget of this many micros instead")
	fs.Int64Var(&estimateMicros, "run-estimate-micros", 0, "with --cap-micros: what each run sets aside, in micros")
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
	capped := capMicros != 0 || estimateMicros != 0
	switch {
	case capped && confirm:
		return usagef("--cap-micros and --confirm-unlimited are two different budgets")
	case !confirm && !capped && (noAlert || alertMicros != 0):
		return usagef("--alert-micros and --no-alert need --confirm-unlimited or --cap-micros")
	}
	e.Budget = install.BudgetChoice{Confirmed: confirm || capped, AlertOff: noAlert, AlertThresholdMicros: core.Micros(alertMicros),
		CapMicros: core.Micros(capMicros), RunEstimateMicros: core.Micros(estimateMicros)}
	if err := e.Budget.Validate(); err != nil {
		return usagef("%v", err)
	}
	e.Getenv, e.Clock = os.Getenv, core.SystemClock{}
	if err := install.RunBootstrap(ctx, e); err != nil {
		return err
	}
	return env.Printf("state store of %s is ready\n", factoryID)
}

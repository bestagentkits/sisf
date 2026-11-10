package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/internal/supervisor/killswitch"
	"github.com/bestagentkits/sisf/pkg/core"
)

// emergencyStopTimeout bounds how long the command waits for the store and
// the supervisor.
const emergencyStopTimeout = 30 * time.Second

func init() {
	register(Command{
		Path:    []string{"emergency-stop"},
		Args:    "--factory-id id --reason text [--actor id] [--admin addr] [--state-dsn-env NAME] [--status | --lift]",
		Summary: "Stop the factory now: no claims, cancel runs, revoke run secrets, freeze publishing",
		Help: `
Stops the factory without the control plane or the dispatcher. The stop is
recorded straight in the state store when its connection is in the
environment (` + killswitch.EnvStateDSN + ` by default): that alone freezes
publishing and holds every issue so nothing can be claimed. The supervisor is
then asked on its admin socket to do the rest: cancel the runs in flight and
revoke their secrets. Stopping again is safe.

Exits 1 when a route could not be used, for example when the supervisor did
not answer; what was done is printed as JSON either way.

--status asks the supervisor whether a stop is in force.
--lift ends the stop through the store; the supervisor socket cannot lift one.`,
		Run: runEmergencyStop,
	})
}

// emergencyStopFlags are the parsed flags of the command.
type emergencyStopFlags struct {
	factoryID, actor, reason, admin, dsnEnv string
	status, lift                            bool
}

func runEmergencyStop(ctx context.Context, env *Env, args []string) error {
	var f emergencyStopFlags
	fs := newFlagSet("emergency-stop")
	fs.StringVar(&f.factoryID, "factory-id", "", "factory id of the deployment to stop")
	fs.StringVar(&f.actor, "actor", "", "who asks, for the audit log (default user:$USER)")
	fs.StringVar(&f.reason, "reason", "", "why the factory is stopped or the stop is lifted")
	fs.StringVar(&f.admin, "admin", supervisor.DefaultAdminAddr, "supervisor admin socket: unix:/absolute/path or a loopback host:port; empty skips it")
	fs.StringVar(&f.dsnEnv, "state-dsn-env", killswitch.EnvStateDSN, "environment variable holding the state store connection")
	fs.BoolVar(&f.status, "status", false, "only report whether a stop is in force")
	fs.BoolVar(&f.lift, "lift", false, "lift the stop in force")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if f.actor == "" {
		if user := strings.TrimSpace(os.Getenv("USER")); user != "" {
			f.actor = "user:" + user
		}
	}
	ctx, cancel := context.WithTimeout(ctx, emergencyStopTimeout)
	defer cancel()
	switch {
	case f.status && f.lift:
		return usagef("--status and --lift cannot be combined")
	case f.status:
		return printFreezeStatus(ctx, env, f.admin)
	case f.factoryID == "" || f.reason == "" || f.actor == "":
		return usagef("--factory-id, --reason and an actor (--actor or $USER) are required")
	}
	factory := core.FactoryID(f.factoryID)
	if err := factory.Validate(); err != nil {
		return usagef("--factory-id: %v", err)
	}
	dsn := core.NewSecretValue([]byte(os.Getenv(f.dsnEnv)))
	if f.lift {
		if err := killswitch.LiftFromOperator(ctx, factory, dsn, core.SystemClock{}, f.actor, f.reason); err != nil {
			return err
		}
		return env.Printf("emergency stop lifted\n")
	}
	report, stopErr := killswitch.StopFromOperator(ctx, killswitch.OperatorRequest{
		FactoryID: factory, Actor: f.actor, Reason: f.reason, StateDSN: dsn, AdminAddr: f.admin, Clock: core.SystemClock{},
	})
	if report.Store != nil || report.Supervisor != nil || len(report.Failures) > 0 {
		if err := printJSON(env, report); err != nil {
			return err
		}
	}
	return stopErr
}

// printFreezeStatus asks the supervisor whether a stop is in force.
func printFreezeStatus(ctx context.Context, env *Env, addr string) error {
	client, err := supervisor.NewClient(addr)
	if err != nil {
		return usagef("--admin: %v", err)
	}
	status, err := client.FreezeStatus(ctx)
	if err != nil {
		return err
	}
	return printJSON(env, status)
}

// printJSON writes v to standard output as indented JSON.
func printJSON(env *Env, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return core.NewError(core.CodeInternal, "the result cannot be encoded").WithCause(err)
	}
	return env.Printf("%s\n", raw)
}

package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/install"
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
		Args:    "--reason text [--dir path | --factory-id id] [--actor id] [--admin addr] [--state-dsn-env NAME] [--status | --lift]",
		Summary: "Stop the factory now: no claims, cancel runs, revoke run secrets, freeze publishing",
		Help: `
Stops the factory without the control plane or the dispatcher. The stop is
recorded straight in the state store: that alone freezes publishing and
holds every issue so nothing can be claimed. The supervisor is then asked on
its admin socket to do the rest: cancel the runs in flight and revoke their
secrets. Stopping again is safe.

With an install on this host, name it and nothing else is needed:
  sisf emergency-stop --dir <install> --reason "<why>"
reads the factory id from <install>/factory.yaml, the state store from the
control zone's secrets and the admin address of a Standalone install. Without
--dir and --factory-id, the install in $` + homeEnv + ` or ~/.sisf/factory is used.
A Docker install keeps its admin socket and store inside the Compose
project; stop it from the dashboard or with sisf call factory.emergency_stop.

For a factory that is not installed here, name it with --factory-id, give
the store connection in $` + killswitch.EnvStateDSN + ` (or the variable --state-dsn-env
names) and the admin address with --admin.

Before anything is sent to the supervisor, it must prove that it serves this
factory (and, with an install, this install's root key): another factory on
the same host may hold the default admin address ` + supervisor.DefaultAdminAddr + `. A
supervisor also refuses a stop for a factory other than its own.

Exits 1 when a route could not be used, for example when the supervisor did
not answer; what was done is printed as JSON either way.

--status asks the supervisor whether a stop is in force.
--lift ends the stop through the store; the supervisor socket cannot lift one.`,
		Run: runEmergencyStop,
	})
}

// emergencyStopFlags are the parsed flags of the command.
type emergencyStopFlags struct {
	dir, factoryID, actor, reason, admin, dsnEnv string
	status, lift                                 bool
}

func runEmergencyStop(ctx context.Context, env *Env, args []string) error {
	var f emergencyStopFlags
	fs := newFlagSet("emergency-stop")
	fs.StringVar(&f.dir, "dir", "", "install directory to read the factory id, the store and the admin address from (default $"+homeEnv+" or ~/.sisf/factory)")
	fs.StringVar(&f.factoryID, "factory-id", "", "factory id of the deployment to stop, when it is not installed here")
	fs.StringVar(&f.actor, "actor", "", "who asks, for the audit log (default user:$USER)")
	fs.StringVar(&f.reason, "reason", "", "why the factory is stopped or the stop is lifted")
	fs.StringVar(&f.admin, "admin", "", "supervisor admin socket: unix:/absolute/path or a loopback host:port (default "+supervisor.DefaultAdminAddr+"); \"none\" skips it")
	fs.StringVar(&f.dsnEnv, "state-dsn-env", killswitch.EnvStateDSN, "environment variable holding the state store connection")
	fs.BoolVar(&f.status, "status", false, "only report whether a stop is in force")
	fs.BoolVar(&f.lift, "lift", false, "lift the stop in force")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if f.status && f.lift {
		return usagef("--status and --lift cannot be combined")
	}
	target, err := stopTarget(f)
	if err != nil {
		return err
	}
	if f.actor == "" {
		if user := strings.TrimSpace(os.Getenv("USER")); user != "" {
			f.actor = "user:" + user
		}
	}
	ctx, cancel := context.WithTimeout(ctx, emergencyStopTimeout)
	defer cancel()
	if f.status {
		return printFreezeStatus(ctx, env, target)
	}
	if missing := missingStopInput(f, target); missing != "" {
		return usagef("%s", missing)
	}
	if f.lift {
		if err := killswitch.LiftFromOperator(ctx, target.FactoryID, target.StateDSN, core.SystemClock{}, f.actor, f.reason); err != nil {
			return err
		}
		return env.Printf("emergency stop of %s lifted\n", target.FactoryID)
	}
	report, stopErr := killswitch.StopFromOperator(ctx, killswitch.OperatorRequest{
		FactoryID: target.FactoryID, Actor: f.actor, Reason: f.reason, StateDSN: target.StateDSN,
		AdminAddr: target.AdminAddr, AdminKeyID: target.RootKeyID, Clock: core.SystemClock{},
	})
	if report.Store != nil || report.Supervisor != nil || len(report.Failures) > 0 {
		if err := printJSON(env, report); err != nil {
			return err
		}
	}
	return stopErr
}

// stopTarget decides which factory the command acts on: the one named by
// --factory-id with the store from the environment, or the install in --dir
// (default the SISF home) with everything read from it. Flags given beside
// an install override what was read, except a factory id that differs.
func stopTarget(f emergencyStopFlags) (install.StopTarget, error) {
	var t install.StopTarget
	if f.factoryID == "" || f.dir != "" {
		dir, err := resolveDir(f.dir)
		if err != nil {
			return t, err
		}
		read, err := install.ReadStopTarget(dir)
		switch {
		case err == nil:
			t = read
		case f.dir != "" || !core.HasCode(err, core.CodeNotFound):
			return t, err
		}
	}
	if f.factoryID != "" {
		id := core.FactoryID(f.factoryID)
		if err := id.Validate(); err != nil {
			return t, usagef("--factory-id: %s", errorMessage(err))
		}
		if t.FactoryID != "" && t.FactoryID != id {
			return t, usagef("--factory-id %s does not match the install in --dir, which is factory %s", id, t.FactoryID)
		}
		t.FactoryID = id
	}
	if dsn := os.Getenv(f.dsnEnv); dsn != "" {
		t.StateDSN = core.NewSecretValue([]byte(dsn))
	}
	switch f.admin {
	case "":
		if t.AdminAddr == "" {
			t.AdminAddr = supervisor.DefaultAdminAddr
		}
	case "none":
		t.AdminAddr = ""
	default:
		t.AdminAddr = f.admin
	}
	return t, nil
}

// missingStopInput names exactly what a stop or a lift still needs, or ""
// when nothing is missing.
func missingStopInput(f emergencyStopFlags, t install.StopTarget) string {
	var missing []string
	if t.FactoryID == "" {
		missing = append(missing, "the factory: pass --dir of an install or --factory-id")
	}
	if strings.TrimSpace(f.reason) == "" {
		missing = append(missing, "the reason: pass --reason")
	}
	if f.actor == "" {
		missing = append(missing, "who asks: pass --actor, since $USER is not set")
	}
	if f.lift && t.StateDSN.IsZero() {
		missing = append(missing, "the state store, which a lift needs: pass --dir of the install or set $"+f.dsnEnv)
	}
	if !f.lift && t.StateDSN.IsZero() && t.AdminAddr == "" {
		missing = append(missing, "a route to the factory: pass --dir of the install, set $"+f.dsnEnv+" or give the supervisor's address with --admin")
	}
	if len(missing) == 0 {
		return ""
	}
	return "missing " + strings.Join(missing, "; ")
}

// printFreezeStatus asks the supervisor whether a stop is in force, after it
// proved it is the supervisor of the factory named, when one is named. The
// answer names the factory it is about.
func printFreezeStatus(ctx context.Context, env *Env, t install.StopTarget) error {
	if t.AdminAddr == "" {
		return usagef("--status asks the supervisor; give its address with --admin")
	}
	client, err := supervisor.NewClient(t.AdminAddr)
	if err != nil {
		return usagef("--admin: %s", errorMessage(err))
	}
	factory := t.FactoryID
	if factory != "" {
		if err := killswitch.VerifySupervisor(ctx, client, factory, t.RootKeyID); err != nil {
			return err
		}
	} else {
		st, err := client.Status(ctx)
		if err != nil {
			return err
		}
		factory = st.FactoryID
	}
	status, err := client.FreezeStatus(ctx)
	if err != nil {
		return err
	}
	return printJSON(env, struct {
		FactoryID core.FactoryID `json:"factory_id"`
		supervisor.FreezeStatus
	}{factory, status})
}

// printJSON writes v to standard output as indented JSON.
func printJSON(env *Env, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return core.NewError(core.CodeInternal, "the result cannot be encoded").WithCause(err)
	}
	return env.Printf("%s\n", raw)
}

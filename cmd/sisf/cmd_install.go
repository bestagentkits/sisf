package main

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func init() {
	register(Command{
		Path:    []string{"init"},
		Args:    "--repo owner/name (--github-token-env NAME --github-installation-id N --github-app-login LOGIN | --profile sim | --dev --github-credential gh-cli) [sign-in flags] [--mode standalone|docker] [--dir path]",
		Summary: "Create a factory install: identity, sign-in, secrets and manifest",
		Help: `
Creates one factory in a new or empty install directory (default
$` + homeEnv + ` or ~/.sisf/factory): one credential set per zone, a root signing
key, factory.yaml and, in Standalone mode, service units. Nothing is built
or started. The factory id is sisf-<repository name> unless --factory-id
names another; give a second install of a same-named repository on one host
an id of its own. Every option is checked before anything is written or
asked, and a failed init leaves nothing behind.

Every sisf up builds the factory from a source checkout of sisf: the one
--source names, else the checkout that holds the current directory. init
records it in source.json for later starts and for the service units.

Secrets are written to <dir>/secrets with owner-only permissions and are
never printed. The GitHub token is read from the environment variable
--github-token-env names, never from the command line.

People sign in to the dashboard, REST, MCP and the CLI (sisf login) with
OpenID Connect or GitHub; the Owner is granted locally, by identity:
  --external-url URL     origin people reach control at (default http://127.0.0.1:7463)
  --owner ISSUER#SUBJECT the Owner's sign-in identity (repeatable)
  --oidc-issuer URL --oidc-client-id ID [--oidc-client-secret-file F | --oidc-client-secret-env NAME]
  --github-login-client-id ID (--github-login-client-secret-file F | --github-login-client-secret-env NAME)
     [--github-login-url URL] [--github-login-api-url URL]
Client secrets are read from a file or a variable, never from a flag value.
Outside the simulation, init refuses without a provider and an Owner.

A real install acts for a GitHub App installation, which control and the
supervisor need at every start:
  --github-installation-id N   the installation whose webhooks are accepted
  --github-app-login LOGIN     the account the factory writes as, my-factory[bot]
  [--github-app-id N]          that account's numeric id
--github-token-env names the variable holding that account's token. It is
read at each start and never refreshed, so it must be long-lived; an App
installation token, which lasts an hour, stops working until a restart.

--profile sim runs against a fake GitHub on 127.0.0.1:7467 and signs in at
the fake identity provider on 127.0.0.1:7468, as the Owner "owner".
--dev (Standalone only, loopback only) signs in at that fake provider too,
locked: a browser unlocks sign-in at 127.0.0.1:7468/_sim/unlock with the
secret each ` + "`sisf up`" + ` writes to secrets/control/dev-sign-in, which no agent is
given; with --github-credential gh-cli the GitHub token
is taken from ` + "`gh auth token`" + ` at init and at every start. sisf doctor warns
loudly about a development install.

The default budget mode is unlimited. Confirm it with --confirm-unlimited and
either --alert-threshold AMOUNT or --no-alert, or confirm a cap with
--budget-cap AMOUNT [--alert-threshold AMOUNT] [--run-estimate AMOUNT]. Until
confirmed, the factory takes in issues and starts no work.

A Standalone install can run real agents as local processes under
trusted_code_only: --coder-agent claude|codex [--coder-model M] [--agent-home DIR]
[--workspace-source URL|PATH], --qa-judge agent --qa-agent codex|claude
[--qa-model M] [--qa-home DIR], --build-command CMD, --exec local.
The local backend builds nothing an agent wrote. To build candidates, give
the install a Docker build sandbox the operator runs: --exec docker
--exec-docker-endpoint http://127.0.0.1:PORT (the runner filter, sisf
docker-filter --role runner) --exec-docker-network NET (internal)
--egress-proxy http://HOST:PORT (on NET) --runner-image NAME@sha256:DIGEST
--build-command CMD, and --qa-agent-binary LAUNCHER, an absolute launcher
that runs the QA agent in a sandbox (--qa-home is then refused).`,
		Run: runInit,
	})
	register(Command{
		Path:    []string{"up"},
		Args:    "[--dir path] [init flags]",
		Summary: "Build, verify and start the whole factory (all three zones)",
		Help: `
Starts the factory of the install directory, creating it first when it holds
none (the init flags then apply; see sisf help init). It builds the programs or images, signs the
release manifest, checks the isolation rules, prepares the state store and
starts every unit.

Standalone mode stays in the foreground and supervises the units until
interrupted; SIGHUP restarts them. Docker mode starts one Compose project and
returns. Ports used: 7460-7469 on 127.0.0.1, nothing else.`,
		Run: runUp,
	})
	register(Command{
		Path: []string{"stop"}, Args: "[--dir path]", Summary: "Stop the factory and keep everything it made",
		Run: lifecycle("stop", func(ctx context.Context, in *install.Installer, _ bool) error { return in.Stop(ctx) }),
	})
	register(Command{
		Path: []string{"restart"}, Args: "[--dir path]", Summary: "Restart every unit of the running factory",
		Run: lifecycle("restart", func(ctx context.Context, in *install.Installer, _ bool) error { return in.Restart(ctx) }),
	})
	register(Command{
		Path: []string{"down"}, Args: "[--dir path] [-v]", Summary: "Stop the factory and remove what a run created",
		Help: `
Stops the factory. In Docker mode it also removes the containers and networks
of this install's Compose project. With -v the data is removed too (the
state volume in Docker mode, the data directory in Standalone mode).
Secrets and factory.yaml are never removed by this command.`,
		Run: lifecycle("down", func(ctx context.Context, in *install.Installer, volumes bool) error { return in.Down(ctx, volumes) }),
	})
}

// newInstaller returns the installer of dir, writing progress to env. The
// source checkout is the one --source names, else the one init recorded for
// the install, else the checkout that holds the current directory.
func newInstaller(env *Env, dir, source string, debug bool) (*install.Installer, error) {
	in := install.New(dir, env.Stdout)
	in.Debug = debug
	if source != "" {
		abs, err := filepath.Abs(source)
		if err != nil {
			return nil, usagef("--source %q is not a usable path", source)
		}
		in.SourceDir = abs
		return in, nil
	}
	recorded, err := install.RecordedSource(dir)
	if err != nil {
		return nil, err
	}
	in.SourceDir = recorded
	if in.SourceDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			in.SourceDir = install.FindSource(cwd) // none found is reported when a build needs it
		}
	}
	return in, nil
}

func runUp(ctx context.Context, env *Env, args []string) error {
	var f installFlags
	fs := newFlagSet("up")
	bindInstallFlags(fs, &f)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	dir, err := resolveDir(f.dir)
	if err != nil {
		return err
	}
	in, err := newInstaller(env, dir, f.source, f.debug)
	if err != nil {
		return err
	}
	o, err := upOptions(env, in, f, dir)
	if err != nil {
		return asUsage(err)
	}
	// SIGHUP asks a running Standalone factory to restart its units.
	hup := make(chan os.Signal, 1)
	reload := make(chan struct{}, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for {
			select {
			case <-hup:
				select {
				case reload <- struct{}{}:
				default: // one pending reload is enough
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	o.Reload = reload
	return in.Up(ctx, o)
}

// upOptions decides what `up` is told. An install that exists keeps its mode
// and profile unless the flags name different ones, which the installer then
// refuses; the repository and token are already in the install, so the init
// flags that describe them are not needed.
func upOptions(env *Env, in *install.Installer, f installFlags, dir string) (install.UpOptions, error) {
	var o install.UpOptions
	choice, given, err := f.budget()
	if err != nil {
		return o, err
	}
	if given {
		o.Budget = &choice
	}
	if m, loadErr := in.Layout.LoadManifest(); loadErr == nil {
		if f.mode == "" {
			f.mode = string(m.Mode)
		}
		if f.profile == "" && m.Extensions["profile"] == "sim" {
			f.profile = "sim"
		}
		if f.profile != "" && f.profile != "sim" {
			return o, usagef("--profile must be \"sim\" or empty")
		}
		o.Init = install.Options{Dir: dir, Mode: manifest.Mode(f.mode), Sim: f.profile == "sim"}
		return o, nil
	}
	if f.mode == "" {
		f.mode = string(manifest.ModeStandalone)
	}
	if o.Init, err = f.options(dir, os.Getenv); err != nil {
		return o, err
	}
	// A new install is checked whole before the Owner is asked about money.
	if _, err := in.Preflight(o.Init); err != nil {
		return o, err
	}
	if given {
		o.Init.Budget = choice
		return o, nil
	}
	o.Init.Budget, err = budgetFor(env, f)
	return o, err
}

// lifecycle builds a command that acts on an existing install.
func lifecycle(name string, act func(context.Context, *install.Installer, bool) error) func(context.Context, *Env, []string) error {
	return func(ctx context.Context, env *Env, args []string) error {
		var dir string
		var volumes bool
		fs := newFlagSet(name)
		fs.StringVar(&dir, "dir", "", "install directory (default $"+homeEnv+" or ~/.sisf/factory)")
		if name == "down" {
			fs.BoolVar(&volumes, "v", false, "also remove the data")
			fs.BoolVar(&volumes, "volumes", false, "also remove the data")
		}
		if err := parseFlags(fs, args); err != nil {
			return err
		}
		abs, err := resolveDir(dir)
		if err != nil {
			return err
		}
		in, err := newInstaller(env, abs, "", false)
		if err != nil {
			return err
		}
		if _, err := in.Layout.LoadManifest(); err != nil {
			if core.HasCode(err, core.CodeNotFound) {
				return core.NewError(core.CodeNotFound, abs+" holds no install (no factory.yaml)",
					core.NextAction{Action: "sisf init --dir " + abs, Description: "create an install there"},
					core.NextAction{Action: "--dir", Description: "name the directory of an existing install"})
			}
			return err
		}
		wasRunning, err := in.Running()
		if err != nil {
			return err
		}
		if err := act(ctx, in, volumes); err != nil {
			return err
		}
		return env.Printf("%s\n", lifecycleDone(name, abs, volumes, wasRunning || isDocker(in)))
	}
}

// isDocker reports whether the install runs in Docker mode, where Compose,
// not a pid file, knows whether the factory ran.
func isDocker(in *install.Installer) bool {
	m, err := in.Layout.LoadManifest()
	return err == nil && m.Mode == manifest.ModeDocker
}

// lifecycleDone is the line a lifecycle command prints once it succeeded, so
// a command that changed the running factory never ends in silence, and one
// that found it stopped says so.
func lifecycleDone(name, dir string, volumes, wasRunning bool) string {
	switch {
	case name == "restart":
		return "the factory in " + dir + " is restarting every unit"
	case !wasRunning && name == "down" && volumes:
		return "the factory in " + dir + " was not running; its data is removed"
	case !wasRunning:
		return "the factory in " + dir + " was not running; nothing to stop"
	case name == "down" && volumes:
		return "the factory in " + dir + " is down and its data is removed"
	case name == "down":
		return "the factory in " + dir + " is down"
	default:
		return "the factory in " + dir + " is stopped"
	}
}

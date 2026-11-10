// Command supervisor is the trusted release supervisor of one factory
// deployment. It is a separate binary, built into its own image, so that no
// release of another unit can replace it.
//
// Commands:
//
//	supervisor run     check this supervisor against the signed release
//	                   manifest, then serve status on a local socket and,
//	                   given the state store, the emergency stop; given
//	                   the factory manifest too, build, evaluate and
//	                   release candidates and serve the Owner rollback
//	supervisor digest  print the digest this supervisor measures of itself,
//	                   the value a release manifest pins
//	supervisor status  ask a running supervisor for its status
//
// "run" refuses to start, with exit status 1, when it cannot prove that it
// is the supervisor the Owner signed for, when it was given a state store
// it cannot open, or when the lifecycle it was asked to run cannot be wired.
//
// Exit status: 0 on success, 1 when a command fails or the supervisor
// refuses to start, 2 when it was invoked wrongly.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/observe"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/internal/supervisor/killswitch"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Exit statuses of the program.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// program is the name the binary is invoked by.
const program = "supervisor"

// statusTimeout bounds how long "status" waits for a supervisor to answer.
const statusTimeout = 5 * time.Second

const usageText = `supervisor is the trusted release supervisor of one factory deployment.

Usage:
  supervisor run --factory-id ID --manifest FILE --root-key-ref REF --trust-dir DIR [--lkg FILE] [--admin ADDR] [--state-dsn-env NAME]
                 [--dispatcher-admin-url URL] [--factory-manifest FILE --edge-admin-url URL --deploy local|compose ...]
  supervisor digest
  supervisor status [--admin ADDR]

Commands:
  run     Check this supervisor against the signed release manifest, then serve status
  digest  Print the digest this supervisor measures of itself
  status  Ask a running supervisor for its status

ADDR is unix:/absolute/path or a loopback host:port (default ` + supervisor.DefaultAdminAddr + `).
REF locates the root public key, for example env:SISF_ROOT_PUBLIC_KEY.
`

// env is where a command reads its surroundings and writes its output.
// Commands use it instead of the process globals so they can run in tests.
type env struct {
	stdout    io.Writer
	stderr    io.Writer
	lookupEnv func(string) (string, bool)
}

// say writes help or a diagnostic. A failed write is dropped on purpose: the
// stream that would carry the complaint is the one that failed, and the exit
// status already tells the caller what happened.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	status := run(ctx, os.Args[1:], env{stdout: os.Stdout, stderr: os.Stderr, lookupEnv: os.LookupEnv})
	stop()
	os.Exit(status)
}

// run executes the command line args and returns the exit status.
func run(ctx context.Context, args []string, e env) int {
	if len(args) == 0 {
		say(e.stderr, "%s", usageText)
		return exitUsage
	}
	switch args[0] {
	case "run":
		return runSupervisor(ctx, args[1:], e)
	case "digest":
		return printDigest(args[1:], e)
	case "status":
		return printStatus(ctx, args[1:], e)
	case "help", "-h", "-help", "--help":
		say(e.stdout, "%s", usageText)
		return exitOK
	}
	say(e.stderr, "%s: unknown command %q\n\n%s", program, args[0], usageText)
	return exitUsage
}

// parseFlags parses a command's flags and rejects stray arguments. ok is
// false when the command must stop with the returned status.
func parseFlags(flags *flag.FlagSet, args []string, e env) (status int, ok bool) {
	flags.SetOutput(e.stderr)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK, false
		}
		return exitUsage, false
	}
	if flags.NArg() > 0 {
		say(e.stderr, "%s: unexpected argument %q\n", flags.Name(), flags.Arg(0))
		return exitUsage, false
	}
	return exitOK, true
}

// runSupervisor runs the startup checks and serves status until interrupted.
func runSupervisor(ctx context.Context, args []string, e env) int {
	var cfg supervisor.Config
	var factoryID, dsnEnv string
	var lf lifecycleFlags
	flags := flag.NewFlagSet(program+" run", flag.ContinueOnError)
	lf.register(flags)
	flags.StringVar(&factoryID, "factory-id", "", "factory_id of the deployment this supervisor belongs to")
	flags.StringVar(&cfg.ManifestPath, "manifest", "", "path of the signed release manifest")
	flags.StringVar(&cfg.RootKeyRef, "root-key-ref", "", "root public key reference, for example env:SISF_ROOT_PUBLIC_KEY")
	flags.StringVar(&cfg.TrustDir, "trust-dir", "", "directory of the trust volume, which only the supervisor writes")
	flags.StringVar(&cfg.LKGPath, "lkg", "", "last known good record, inside the trust volume (default <trust-dir>/"+supervisor.DefaultLKGFile+")")
	flags.StringVar(&cfg.AdminAddr, "admin", supervisor.DefaultAdminAddr, "status socket: unix:/absolute/path or a loopback host:port")
	flags.StringVar(&dsnEnv, "state-dsn-env", killswitch.EnvStateDSN, "environment variable holding the state store connection; when it is set the socket also serves the emergency stop")
	if status, ok := parseFlags(flags, args, e); !ok {
		return status
	}
	cfg.FactoryID = core.FactoryID(factoryID)

	clock := core.SystemClock{}
	proc, err := observe.Start(e.stderr, getenv(e.lookupEnv), clock)
	if err != nil {
		printError(e.stderr, err)
		return exitFailure
	}
	defer func() { _ = proc.Shutdown(context.WithoutCancel(ctx)) }()
	stop, err := openEmergencyStop(ctx, cfg.FactoryID, dsnEnv, lf.dispatcherAdminURL, e.lookupEnv, clock)
	if err != nil {
		say(e.stderr, "%s: refusing to start: the emergency stop needs the state store\n", program)
		printError(e.stderr, err)
		return exitFailure
	}
	if stop != nil {
		defer stop.close()
	}
	opts := stop.options(supervisor.Options{LookupEnv: e.lookupEnv, Clock: clock})
	stages, err := lf.startLifecycle(ctx, lifecycleDeps{cfg: cfg, stop: stop, lookupEnv: e.lookupEnv,
		redactor: proc.Redactor, log: proc.Log, clock: clock}, &opts)
	if err != nil {
		say(e.stderr, "%s: refusing to start: the lifecycle cannot be wired\n", program)
		printError(e.stderr, err)
		return exitFailure
	}
	sup, err := supervisor.Start(ctx, cfg, opts)
	if err != nil {
		say(e.stderr, "%s: refusing to start\n", program)
		printError(e.stderr, err)
		return exitFailure
	}
	// Enforcement and the stages end before the store they use is closed.
	enforceCtx, stopEnforcing := context.WithCancel(ctx)
	enforced := make(chan struct{})
	go func() {
		defer close(enforced)
		staged := make(chan struct{})
		go func() {
			defer close(staged)
			if stages != nil {
				stages(enforceCtx)
			}
		}()
		stop.enforce(enforceCtx, clock, e.stderr)
		<-staged
	}()
	defer func() {
		stopEnforcing()
		<-enforced
	}()
	status := sup.Status()
	say(e.stderr, "%s: self-check passed: executable %s is the one pinned by the release manifest signed with %s\n",
		program, status.SelfCheck.ExecutableDigest, status.SelfCheck.ManifestKeyID)
	say(e.stderr, "%s: serving status on admin socket %s\n", program, sup.AdminAddr())
	if err := sup.Serve(ctx); err != nil {
		printError(e.stderr, err)
		return exitFailure
	}
	say(e.stderr, "%s: stopped\n", program)
	return exitOK
}

// printDigest prints the digest the supervisor measures of itself, which is
// the value to pin as supervisor_executable in a release manifest.
func printDigest(args []string, e env) int {
	if status, ok := parseFlags(flag.NewFlagSet(program+" digest", flag.ContinueOnError), args, e); !ok {
		return status
	}
	digest, err := supervisor.MeasureExecutable()
	if err != nil {
		printError(e.stderr, err)
		return exitFailure
	}
	if _, err := fmt.Fprintln(e.stdout, digest); err != nil {
		printError(e.stderr, core.NewError(core.CodeUnavailable, "the digest could not be written to standard output").WithCause(err))
		return exitFailure
	}
	return exitOK
}

// printStatus asks a running supervisor for its status and prints it as
// JSON. It exits 1 when no supervisor answers, which makes it usable as a
// health check in an image that has no other tools.
func printStatus(ctx context.Context, args []string, e env) int {
	var addr string
	flags := flag.NewFlagSet(program+" status", flag.ContinueOnError)
	flags.StringVar(&addr, "admin", supervisor.DefaultAdminAddr, "status socket: unix:/absolute/path or a loopback host:port")
	if status, ok := parseFlags(flags, args, e); !ok {
		return status
	}
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	status, err := supervisor.FetchStatus(ctx, addr)
	if err != nil {
		printError(e.stderr, err)
		return exitFailure
	}
	encoder := json.NewEncoder(e.stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(status); err != nil {
		printError(e.stderr, core.NewError(core.CodeUnavailable, "the status could not be written to standard output").WithCause(err))
		return exitFailure
	}
	return exitOK
}

// printError writes a failure as its error code and message, followed by the
// underlying cause when there is one. Causes here are operating-system and
// decoding errors; key material is never part of an error.
func printError(w io.Writer, err error) {
	var typed *core.Error
	if !errors.As(err, &typed) {
		say(w, "%s: %s: %v\n", program, core.CodeInternal, err)
		return
	}
	say(w, "%s: %s\n", program, typed.Error())
	if cause := typed.Unwrap(); cause != nil {
		say(w, "  cause: %v\n", cause)
	}
}

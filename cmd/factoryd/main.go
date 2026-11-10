// Command factoryd runs the factory's own units: the control plane of Zone 3
// and the dispatcher of Zone 2. One binary serves both; the subcommand picks
// the role, and each role reads only the env file of its own zone.
//
//	factoryd control      serve the dashboard, REST, MCP, WebMCP, sign-in and the webhook route
//	factoryd dispatcher   claim Ready issues and drive their runs
//
// Owner commands, run on the factory's host with the control zone's
// environment (in Docker mode: docker compose exec control factoryd ...):
//
//	factoryd policy request --revision SHA
//	factoryd policy approve --revision SHA [--approval ID]
//	factoryd issue retry --issue owner/repo#N
//
// A report about a run, with the agent zone's environment:
//
//	factoryd report --issue owner/repo#N --run ID --owner NAME --token N --pr REF
//
// Configuration comes from the environment: SISF_CONFIG, SISF_LISTEN,
// SISF_STATE_DSN, SISF_WEBHOOK_SECRET, SISF_GITHUB_TOKEN, the sign-in
// variables (SISF_EXTERNAL_URL, SISF_OWNER_IDENTITIES, SISF_AUTH_*) and the
// others named in package wiring. Secrets are never taken from the
// command line and never printed.
//
// Exit status: 0 on success, 1 on a failure, 2 when invoked wrongly.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/cmd/supervisor/observe"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Exit statuses of the program.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

const usage = `usage:
  factoryd control
  factoryd dispatcher [--agent fake|claude|codex|pi] [--fake-agent-work DURATION] [--fake-agent-patch FILE]
                      [--workspace-root DIR --workspace-source REPO --artifact-dir DIR] [--sandboxed]
                      [--agent-binary LAUNCHER] [--agent-env NAME,...] [--agent-home DIR]
                      [--admin-listen ADDR] [--run-estimate-micros N]
  factoryd policy request --revision SHA
  factoryd policy approve --revision SHA [--approval ID]
  factoryd issue retry --issue owner/repo#N
  factoryd report --issue owner/repo#N --run ID --owner NAME --token N (--pr REF)...
  factoryd approval-public-key
`

// errUsage marks a command line that cannot be run.
var errUsage = errors.New("usage")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	status := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(status)
}

// run executes one command line and returns the exit status.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	proc, err := observe.Start(stderr, getenv, core.SystemClock{}, logSecrets(getenv, os.Environ)...)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "factoryd: %s\n", describe(err))
		return exitFailure
	}
	defer func() { _ = proc.Shutdown(context.WithoutCancel(ctx)) }()
	err = dispatch(ctx, args, getenv, stdout, proc)
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, flag.ErrHelp):
		_, _ = io.WriteString(stdout, usage)
		return exitOK
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintf(stderr, "factoryd: %v\n%s", err, usage)
		return exitUsage
	}
	_, _ = fmt.Fprintf(stderr, "factoryd: %s\n", describe(err))
	return exitFailure
}

// logSecrets are the values, beyond observe.SecretEnv, the process's log
// redacts: the edge counters token, the sign-in client secrets, and every
// value the control zone's env secret provider serves.
func logSecrets(getenv func(string) string, environ func() []string) []string {
	var out []string
	for _, name := range append([]string{wiring.EnvEdgeCountersToken}, wiring.IdentitySecretEnv...) {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			out = append(out, v)
		}
	}
	for _, kv := range environ() {
		name, v, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(name, wiring.EnvControlSecretPrefix) && strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

// dispatch runs the subcommand named by args.
func dispatch(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer, proc *observe.Process) error {
	log := proc.Log
	if len(args) == 0 {
		return fmt.Errorf("%w: a subcommand is required", errUsage)
	}
	switch name, rest := args[0], args[1:]; {
	case name == "control":
		return runControl(ctx, rest, getenv, proc)
	case name == "dispatcher":
		return runDispatcher(ctx, rest, getenv, log)
	case name == "policy" && len(rest) > 0 && rest[0] == "request":
		return runPolicyRequest(ctx, rest[1:], getenv, stdout, log)
	case name == "policy" && len(rest) > 0 && rest[0] == "approve":
		return runPolicyApprove(ctx, rest[1:], getenv, stdout, log)
	case name == "issue" && len(rest) > 0 && rest[0] == "retry":
		return runIssueRetry(ctx, rest[1:], getenv, stdout, log)
	case name == "report":
		return runReport(ctx, rest, getenv, stdout)
	case name == "approval-public-key":
		return runApprovalPublicKey(rest, getenv, stdout)
	case name == "-h" || name == "--help" || name == "help":
		return flag.ErrHelp
	}
	return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
}

// describe renders a failure for the operator: the code and the message of
// a typed error, which are written for callers and carry no secret.
func describe(err error) string {
	var typed *core.Error
	if !errors.As(err, &typed) {
		return err.Error()
	}
	text := string(typed.Code) + ": " + typed.Message
	for _, next := range typed.NextActions {
		text += "\n  next: " + next.Description
	}
	return text
}

// newFlags returns a flag set whose problems are usage errors.
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parse parses args and refuses leftovers.
func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	}
	return nil
}

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/core"
)

// invocation is what a test command saw when it ran.
type invocation struct {
	name string
	args []string
}

// testRegistry holds commands shaped like the ones later areas add: plain
// ones, two that share a leading word, and ones that fail in each way.
func testRegistry(ran *[]invocation) *registry {
	record := func(name string, err error) func(context.Context, *Env, []string) error {
		return func(_ context.Context, env *Env, args []string) error {
			*ran = append(*ran, invocation{name, args})
			if failed := env.Printf("ran %s\n", name); failed != nil {
				return failed
			}
			return err
		}
	}
	r := &registry{}
	r.add(Command{Path: []string{"up"}, Args: "[--mode mode]", Summary: "Start the deployment", Help: "Starts every unit.", Run: record("up", nil)})
	r.add(Command{Path: []string{"status"}, Summary: "Show the deployment", Run: record("status", nil)})
	r.add(Command{Path: []string{"sim", "oidc"}, Summary: "Run the fake identity provider", Run: record("sim oidc", nil)})
	r.add(Command{Path: []string{"sim", "github"}, Summary: "Run the fake GitHub", Run: record("sim github", nil)})
	r.add(Command{Path: []string{"down"}, Args: "[-v]", Summary: "Stop the deployment", Run: record("down", usagef("unknown flag %s", "-x"))})
	r.add(Command{Path: []string{"doctor"}, Summary: "Check the deployment", Run: record("doctor",
		core.NewError(core.CodeUnavailable, "the state store cannot be reached", core.NextAction{Action: "factory.up", Description: "start the deployment"}))})
	r.add(Command{Path: []string{"explain"}, Summary: "Explain a run", Run: record("explain", flag.ErrHelp)})
	r.add(Command{Path: []string{"crash"}, Summary: "Fail without a code", Run: record("crash", errors.New("disk on fire"))})
	r.add(Command{Path: []string{"probe"}, Summary: "Exit with a chosen status", Run: record("probe", statusError(7))})
	return r
}

// statusError is an error that chooses the exit status.
type statusError int

func (e statusError) Error() string { return fmt.Sprintf("probe failed with status %d", int(e)) }
func (e statusError) ExitCode() int { return int(e) }

// invoke runs args against the test registry.
func invoke(t *testing.T, args ...string) (status int, stdout, stderr string, ran []invocation) {
	t.Helper()
	var out, errOut bytes.Buffer
	status = testRegistry(&ran).run(context.Background(), &Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut}, args)
	return status, out.String(), errOut.String(), ran
}

func TestACommandIsSelectedByItsPathAndGetsTheRemainingArguments(t *testing.T) {
	status, stdout, _, ran := invoke(t, "up", "--mode", "docker")
	if status != exitOK || len(ran) != 1 || ran[0].name != "up" || strings.Join(ran[0].args, " ") != "--mode docker" || stdout != "ran up\n" {
		t.Fatalf("status %d, ran %+v, stdout %q", status, ran, stdout)
	}
	// Commands sharing a leading word are told apart by the next one.
	for _, sub := range []string{"github", "oidc"} {
		status, _, _, ran = invoke(t, "sim", sub, "--port", "7467")
		if status != exitOK || len(ran) != 1 || ran[0].name != "sim "+sub || len(ran[0].args) != 2 {
			t.Fatalf("sim %s: status %d, ran %+v", sub, status, ran)
		}
	}
}

func TestAnInvocationThatSelectsNoCommandExitsWithUsage(t *testing.T) {
	status, stdout, stderr, ran := invoke(t)
	if status != exitUsage || stdout != "" || !strings.Contains(stderr, "Usage:") || len(ran) != 0 {
		t.Errorf("no arguments: status %d, stdout %q, stderr %q", status, stdout, stderr)
	}
	status, _, stderr, ran = invoke(t, "launch")
	if status != exitUsage || !strings.Contains(stderr, `unknown command "launch"`) || len(ran) != 0 {
		t.Errorf("unknown command: status %d, stderr %q", status, stderr)
	}
	// A group is not a command; its members are listed, the rest is not.
	status, _, stderr, ran = invoke(t, "sim")
	if status != exitUsage || !strings.Contains(stderr, "sim github") || !strings.Contains(stderr, "sim oidc") || strings.Contains(stderr, "doctor") || len(ran) != 0 {
		t.Errorf("group alone: status %d, stderr %q", status, stderr)
	}
	status, _, stderr, _ = invoke(t, "sim", "gitlab")
	if status != exitUsage || !strings.Contains(stderr, "sim github") {
		t.Errorf("unknown member of a group: status %d, stderr %q", status, stderr)
	}
}

func TestHelpListsCommandsInOrderAndDescribesOne(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		status, stdout, stderr, ran := invoke(t, args...)
		if status != exitOK || stderr != "" || len(ran) != 0 {
			t.Fatalf("%v: status %d, stderr %q, ran %v", args, status, stderr, ran)
		}
		var previous int
		for _, name := range []string{"crash", "doctor", "down [-v]", "explain", "probe", "sim github", "sim oidc", "status", "up [--mode mode]"} {
			at := strings.Index(stdout, "  "+name)
			if at < previous {
				t.Fatalf("%v: %q is missing or out of order in:\n%s", args, name, stdout)
			}
			previous = at
		}
	}
	for _, args := range [][]string{{"help", "up"}, {"up", "--help"}, {"up", "-h"}} {
		status, stdout, _, ran := invoke(t, args...)
		if status != exitOK || len(ran) != 0 || !strings.Contains(stdout, "sisf up [--mode mode]") || !strings.Contains(stdout, "Starts every unit.") {
			t.Errorf("%v: status %d, ran %v, stdout %q", args, status, ran, stdout)
		}
	}
	if status, stdout, _, _ := invoke(t, "help", "sim"); status != exitOK || !strings.Contains(stdout, "sim oidc") || strings.Contains(stdout, "status") {
		t.Errorf("help for a group lists its members only: status %d, stdout %q", status, stdout)
	}
	if status, _, stderr, _ := invoke(t, "help", "launch"); status != exitUsage || !strings.Contains(stderr, "unknown command") {
		t.Errorf("help for an unknown command: status %d, stderr %q", status, stderr)
	}
	// A command may ask for its own help after parsing its flags.
	if status, stdout, _, ran := invoke(t, "explain", "run-1"); status != exitOK || len(ran) != 1 || !strings.Contains(stdout, "sisf explain") {
		t.Errorf("flag.ErrHelp shows the command's help: status %d, stdout %q", status, stdout)
	}
}

func TestAFailureIsPrintedTypedAndSetsTheExitStatus(t *testing.T) {
	status, _, stderr, _ := invoke(t, "down", "-x")
	if status != exitUsage || !strings.Contains(stderr, "sisf down: unknown flag -x") || !strings.Contains(stderr, "Usage: sisf down [-v]") {
		t.Errorf("a wrong invocation: status %d, stderr %q", status, stderr)
	}
	status, _, stderr, _ = invoke(t, "doctor")
	if status != exitFailure || !strings.Contains(stderr, "sisf: UNAVAILABLE: the state store cannot be reached") ||
		!strings.Contains(stderr, "next: factory.up - start the deployment") {
		t.Errorf("a typed failure: status %d, stderr %q", status, stderr)
	}
	// A failure without a code is never mistaken for success or left untyped.
	status, _, stderr, _ = invoke(t, "crash")
	if status != exitFailure || !strings.Contains(stderr, "sisf: INTERNAL: disk on fire") {
		t.Errorf("an untyped failure: status %d, stderr %q", status, stderr)
	}
	status, _, stderr, _ = invoke(t, "probe")
	if status != 7 || !strings.Contains(stderr, "status 7") {
		t.Errorf("a command may choose its exit status: status %d, stderr %q", status, stderr)
	}
}

func TestErrorDetailsArePrintedInAStableOrder(t *testing.T) {
	failure := core.NewError(core.CodeInvalidArgument, "manifest is not conformant")
	failure.Details = map[string]string{"rules": "zone.missing", "findings": "1"}
	var out bytes.Buffer
	printError(&out, fmt.Errorf("check: %w", failure))
	want := "sisf: INVALID_ARGUMENT: manifest is not conformant\n  findings: 1\n  rules: zone.missing\n"
	if out.String() != want {
		t.Fatalf("printed %q, want %q", out.String(), want)
	}
}

func TestACommandThatCouldNeverRunIsRefusedAtRegistration(t *testing.T) {
	run := func(context.Context, *Env, []string) error { return nil }
	bad := map[string]Command{
		"no path":           {Summary: "s", Run: run},
		"no summary":        {Path: []string{"up"}, Run: run},
		"nothing to run":    {Path: []string{"up"}, Summary: "s"},
		"hides help":        {Path: []string{"help"}, Summary: "s", Run: run},
		"word with a space": {Path: []string{"sim github"}, Summary: "s", Run: run},
		"uppercase word":    {Path: []string{"Up"}, Summary: "s", Run: run},
		"flag as a word":    {Path: []string{"--up"}, Summary: "s", Run: run},
		"registered twice":  {Path: []string{"status"}, Summary: "s", Run: run},
	}
	for name, cmd := range bad {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("registering a command with %s must panic", name)
				}
			}()
			r := &registry{}
			r.add(Command{Path: []string{"status"}, Summary: "Show the deployment", Run: run})
			r.add(cmd)
		})
	}
}

func TestACommandReceivesTheCallersContext(t *testing.T) {
	type key struct{}
	var seen any
	r := &registry{}
	r.add(Command{Path: []string{"up"}, Summary: "Start", Run: func(ctx context.Context, _ *Env, _ []string) error {
		seen = ctx.Value(key{})
		return ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "caller"))
	cancel()
	var errOut bytes.Buffer
	status := r.run(ctx, &Env{Stdout: &bytes.Buffer{}, Stderr: &errOut}, []string{"up"})
	if seen != "caller" || status != exitFailure || !strings.Contains(errOut.String(), "context canceled") {
		t.Fatalf("seen %v, status %d, stderr %q", seen, status, errOut.String())
	}
}

// closedPipe refuses every write, like the output of a reader that has gone.
type closedPipe struct{}

func (closedPipe) Write([]byte) (int, error) { return 0, errors.New("write on closed pipe") }

func TestAResultThatCannotBeWrittenFailsTheCommand(t *testing.T) {
	r := &registry{}
	r.add(Command{Path: []string{"status"}, Summary: "Show the deployment", Run: func(_ context.Context, env *Env, _ []string) error {
		return env.Printf("revision %s is running\n", "r1")
	}})
	var out, errOut bytes.Buffer
	if status := r.run(context.Background(), &Env{Stdout: &out, Stderr: &errOut}, []string{"status"}); status != exitOK || out.String() != "revision r1 is running\n" {
		t.Fatalf("a writable output: status %d, stdout %q, stderr %q", status, out.String(), errOut.String())
	}
	status := r.run(context.Background(), &Env{Stdout: closedPipe{}, Stderr: &errOut}, []string{"status"})
	if status != exitFailure || !strings.Contains(errOut.String(), "sisf: UNAVAILABLE: ") {
		t.Fatalf("an unwritable output: status %d, stderr %q", status, errOut.String())
	}
	// Diagnostics have nowhere else to go; losing them does not change the status.
	if status := r.run(context.Background(), &Env{Stdout: closedPipe{}, Stderr: closedPipe{}}, []string{"launch"}); status != exitUsage {
		t.Fatalf("an unwritable error stream: status %d, want %d", status, exitUsage)
	}
}

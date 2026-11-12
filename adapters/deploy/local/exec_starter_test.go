package local_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/deploy/local"
	"github.com/bestagentkits/sisf/pkg/core"
)

// shell runs a script under /bin/sh whatever the image says, so the test
// needs no compiled program.
func shell(script string) func(string) (string, []string, error) {
	return func(string) (string, []string, error) { return "/bin/sh", []string{"-c", script}, nil }
}

// lines collects what the process writes to stderr.
type lines chan string

func (l lines) next(t *testing.T) string {
	t.Helper()
	select {
	case line := <-l:
		return line
	case <-t.Context().Done():
		t.Fatal("the test ended while waiting for a log line")
		return ""
	}
}

func TestAProcessGetsOnlyTheEnvironmentItIsGivenAndItsCriticalLinesAreCounted(t *testing.T) {
	// Not parallel: it sets a variable in this process's environment.
	t.Setenv("PARENT_SECRET", "hunter2")
	got := make(lines, 16)
	starter := local.ExecStarter{
		Command: shell(`echo "env rev=$SISF_REVISION addr=$LISTEN_ADDR secret=${PARENT_SECRET-unset}" >&2
echo "level=info msg=fine" >&2
echo "level=critical msg=boom" >&2
echo '{"level":"fatal"}' >&2
exec sleep 60`),
		AddrEnv: "LISTEN_ADDR", Env: []string{"PATH=" + os.Getenv("PATH")},
		OnLog: func(line string) { got <- line },
	}
	before := time.Now().Add(-time.Minute)
	proc, err := starter.Start(context.Background(), local.StartSpec{
		Unit: "app", Image: "any", Addr: "127.0.0.1:7461", Env: []string{local.RevisionEnv + "=sha256:abc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !proc.Alive() {
		t.Fatal("the process is not running")
	}
	if line := got.next(t); line != "env rev=sha256:abc addr=127.0.0.1:7461 secret=unset" {
		t.Fatalf("the process saw %q", line)
	}
	for range 3 {
		got.next(t)
	}
	if n := proc.CriticalSince(before); n != 2 {
		t.Fatalf("critical lines = %d, want 2", n)
	}
	if n := proc.CriticalSince(time.Now().Add(time.Hour)); n != 0 {
		t.Fatalf("critical lines in the future = %d, want 0", n)
	}
	if err := proc.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if proc.Alive() {
		t.Fatal("the process is still running after Stop")
	}
	if err := proc.Stop(context.Background()); err != nil {
		t.Fatalf("a second Stop: %v", err)
	}
}

func TestAProcessThatIgnoresTheStopSignalIsKilledWhenTheGraceEnds(t *testing.T) {
	t.Parallel()
	started := make(lines, 1)
	starter := local.ExecStarter{
		Command: shell(`trap '' TERM; echo up >&2; while true; do sleep 1; done`),
		AddrEnv: "LISTEN_ADDR", Env: []string{"PATH=" + os.Getenv("PATH")},
		OnLog: func(line string) { started <- line },
		Clock: firedClock{},
	}
	proc, err := starter.Start(context.Background(), local.StartSpec{Addr: "127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	started.next(t) // the trap is installed before the line is written
	if err := proc.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if proc.Alive() {
		t.Fatal("a process that ignores SIGTERM survived Stop")
	}
}

// firedClock reports every wait as already over, so the stop grace ends at once.
type firedClock struct{}

func (firedClock) Now() time.Time { return time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC) }
func (firedClock) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

func TestAProcessThatExitsIsNotAlive(t *testing.T) {
	t.Parallel()
	done := make(lines, 1)
	starter := local.ExecStarter{Command: shell(`echo bye >&2`), AddrEnv: "LISTEN_ADDR", OnLog: func(l string) { done <- l }}
	proc, err := starter.Start(context.Background(), local.StartSpec{Addr: "127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	done.next(t)
	if err := proc.Stop(context.Background()); err != nil { // waits for the exit
		t.Fatal(err)
	}
	if proc.Alive() {
		t.Fatal("an exited process is alive")
	}
}

func TestTheStarterRefusesWhatTheResolverRefusesAndIncompleteSetups(t *testing.T) {
	t.Parallel()
	refusal := errors.New("image not allowed")
	starter := local.ExecStarter{Command: func(string) (string, []string, error) { return "", nil, refusal }, AddrEnv: "LISTEN_ADDR"}
	if _, err := starter.Start(context.Background(), local.StartSpec{Image: "../../bin/sh"}); !errors.Is(err, refusal) {
		t.Fatalf("Start: %v, want the resolver's refusal", err)
	}
	if _, err := (local.ExecStarter{AddrEnv: "X"}).Start(context.Background(), local.StartSpec{}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("no resolver: %v", err)
	}
	if _, err := (local.ExecStarter{Command: shell("true")}).Start(context.Background(), local.StartSpec{}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("no address variable: %v", err)
	}
	missing := local.ExecStarter{Command: func(string) (string, []string, error) { return "/nonexistent/program", nil, nil }, AddrEnv: "A"}
	if _, err := missing.Start(context.Background(), local.StartSpec{}); err == nil || strings.Contains(err.Error(), "<nil>") {
		t.Fatalf("a missing program started: %v", err)
	}
}

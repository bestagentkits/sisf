package local_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/exec/local"
	"github.com/bestagentkits/sisf/pkg/conformance"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

var start = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

// clock reports each timer a command sets, so a test moves time only once
// the backend waits on it.
type clock struct {
	*sqlstoretest.Clock
	timers chan time.Duration
}

func (c clock) After(d time.Duration) <-chan time.Time {
	ch := c.Clock.After(d)
	c.timers <- d
	return ch
}

func newBackend(t *testing.T) (*local.Backend, clock) {
	t.Helper()
	c := clock{Clock: sqlstoretest.NewClock(start), timers: make(chan time.Duration, 16)}
	b, err := local.New(local.Config{Root: t.TempDir(), Clock: c})
	if err != nil {
		t.Fatal(err)
	}
	return b, c
}

func spec(trust execution.Trust) execution.SandboxSpec {
	return execution.SandboxSpec{RunID: "run-0001", Unit: "runner", Image: "local", Trust: trust,
		Egress: core.EgressPolicy{Mode: core.EgressAllowlist}, Limits: execution.Limits{Timeout: time.Hour},
		Env: map[string]string{"SISF_RUN": "run-0001"}}
}

func wantCode(t *testing.T, err error, code core.ErrorCode, what string) {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("%s: want %s, got %v", what, code, err)
	}
}

func TestUntrustedCodeIsRefused(t *testing.T) {
	t.Parallel()
	b, _ := newBackend(t)
	caps, _ := b.Capabilities(context.Background())
	if execution.UntrustedCodeAllowed(caps) || caps.Supports(core.CapExecIsolation) || caps.Supports(core.CapExecEgressEnforcement) {
		t.Fatalf("the local backend claims isolation or egress enforcement: %v", caps)
	}
	_, err := b.CreateSandbox(context.Background(), spec(execution.TrustUntrusted))
	wantCode(t, err, core.CodeNotSupported, "untrusted code on the local backend")
	for name, mutate := range map[string]func(*execution.SandboxSpec){
		"mounts":       func(s *execution.SandboxSpec) { s.Mounts = []execution.Mount{{Source: "/", Target: "/host"}} },
		"memory limit": func(s *execution.SandboxSpec) { s.Limits.MemoryBytes = 1 << 30 },
		"secrets":      func(s *execution.SandboxSpec) { s.SecretGrantIDs = []string{"grant-1"} },
	} {
		s := spec(execution.TrustTrusted)
		mutate(&s)
		_, err := b.CreateSandbox(context.Background(), s)
		wantCode(t, err, core.CodeNotSupported, name)
	}
}

// The factory's own environment, where control-plane credentials live,
// never reaches a command.
func TestACommandSeesOnlyItsOwnEnvironment(t *testing.T) {
	t.Setenv("SISF_OWNER_TOKEN", "owner-token-value-0001")
	b, _ := newBackend(t)
	sb, err := b.CreateSandbox(context.Background(), spec(execution.TrustTrusted))
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.Exec(context.Background(), sb.ID, execution.Command{Argv: []string{"env"}, Env: map[string]string{"STEP": "build"}})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("env: %+v %v", res, err)
	}
	out := string(res.Stdout)
	if strings.Contains(out, "owner-token-value-0001") || !strings.Contains(out, "SISF_RUN=run-0001") || !strings.Contains(out, "STEP=build") {
		t.Fatalf("unexpected environment:\n%s", out)
	}
}

func TestACommandPastItsTimeIsStopped(t *testing.T) {
	t.Parallel()
	b, c := newBackend(t)
	sb, err := b.CreateSandbox(context.Background(), spec(execution.TrustTrusted))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan execution.ExecResult, 1)
	go func() {
		res, _ := b.Exec(context.Background(), sb.ID, execution.Command{Argv: []string{"sleep", "60"}, Timeout: time.Minute})
		done <- res
	}()
	if d := <-c.timers; d != time.Minute {
		t.Fatalf("the command waits %s", d)
	}
	c.Advance(time.Minute)
	if res := <-done; !res.TimedOut || res.ExitCode == 0 {
		t.Fatalf("the command was not stopped: %+v", res)
	}
	c.Advance(time.Hour)
	_, err = b.Exec(context.Background(), sb.ID, execution.Command{Argv: []string{"true"}})
	wantCode(t, err, core.CodeLimitExceeded, "after the sandbox's time")
}

func TestFilesStayInsideTheWorkspace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, _ := newBackend(t)
	sb, err := b.CreateSandbox(ctx, spec(execution.TrustTrusted))
	if err != nil {
		t.Fatal(err)
	}
	files, err := b.Files(ctx, sb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.WriteFile(ctx, "src/main.go", strings.NewReader("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := files.ReadFile(ctx, "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	_ = r.Close()
	if string(got) != "package main\n" {
		t.Fatalf("read back %q", got)
	}
	res, err := b.Exec(ctx, sb.ID, execution.Command{Argv: []string{"cat", "main.go"}, Dir: "src"})
	if err != nil || string(res.Stdout) != "package main\n" {
		t.Fatalf("cat: %+v %v", res, err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, err := b.Exec(ctx, sb.ID, execution.Command{Argv: []string{"ln", "-s", outside, "link"}}); err != nil || res.ExitCode != 0 {
		t.Fatalf("ln: %+v %v", res, err)
	}
	for _, name := range []string{"../escape.txt", "/etc/passwd", "link", "src/../../escape.txt"} {
		if _, err := files.ReadFile(ctx, name); err == nil {
			t.Errorf("%s was read", name)
		}
		if err := files.WriteFile(ctx, name, strings.NewReader("x"), 0o644); err == nil {
			t.Errorf("%s was written", name)
		}
	}
	if _, err := b.Exec(ctx, sb.ID, execution.Command{Argv: []string{"true"}, Dir: "../"}); err == nil {
		t.Fatal("a command ran outside the workspace")
	}
	if data, _ := os.ReadFile(outside); string(data) != "outside" {
		t.Fatal("a file outside the workspace was changed")
	}
}

func TestTheBackendGoesThroughTheConformanceKitAsTrustedOnly(t *testing.T) {
	t.Parallel()
	b, _ := newBackend(t)
	row, err := conformance.GradeExecution(context.Background(), b, conformance.ExecutionProbe{
		Spec: spec(execution.TrustTrusted), Command: execution.Command{Argv: []string{"true"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(row.Missing) != 1 || row.Missing[0] != core.CapIsolation {
		t.Fatalf("the local backend should lack isolation only: %+v", row)
	}
	_, err = b.Snapshot(context.Background(), "x")
	wantCode(t, err, core.CodeNotSupported, "snapshot")
}

package stages

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// scriptedSandbox is an isolating backend whose sandbox creation and
// command outcome are set by the test.
type scriptedSandbox struct {
	isolating
	createErr error
	result    execution.ExecResult
	execErr   error
	// cancel, when set, is called while the command runs.
	cancel context.CancelFunc
}

func (s scriptedSandbox) CreateSandbox(context.Context, execution.SandboxSpec) (execution.SandboxInfo, error) {
	if s.createErr != nil {
		return execution.SandboxInfo{}, s.createErr
	}
	return execution.SandboxInfo{ID: "sb-1", State: execution.SandboxRunning}, nil
}

func (s scriptedSandbox) Exec(context.Context, execution.SandboxID, execution.Command) (execution.ExecResult, error) {
	if s.cancel != nil {
		s.cancel()
	}
	return s.result, s.execErr
}

func (scriptedSandbox) Destroy(context.Context, execution.SandboxID) error { return nil }

func TestABuildCommandThatRanAndFailedIsTheCandidatesFault(t *testing.T) {
	t.Parallel()
	req := ImageRequest{RunID: "run-1", Unit: "app", Commit: baseSHA, Untrusted: true}
	for name, c := range map[string]struct {
		exec     scriptedSandbox
		want     RefusalReason
		wantCode core.ErrorCode
	}{
		"a non-zero exit": {exec: scriptedSandbox{result: execution.ExecResult{ExitCode: 1}}, want: RefusalBuildFailed, wantCode: core.CodeGuardFailed},
		"killed by a signal": {exec: scriptedSandbox{result: execution.ExecResult{ExitCode: 137}}, want: RefusalBuildFailed,
			wantCode: core.CodeGuardFailed},
		"past its timeout": {exec: scriptedSandbox{result: execution.ExecResult{ExitCode: -1, TimedOut: true}}, want: RefusalBuildTimeout,
			wantCode: core.CodeTimeout},
	} {
		_, err := SandboxImages{Exec: c.exec, Argv: []string{"build"}, Timeout: time.Minute}.BuildImage(context.Background(), req)
		r, ok := RefusalOf(err)
		if !ok || r.Reason != c.want || !core.HasCode(err, c.wantCode) {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := SandboxImages{Exec: scriptedSandbox{result: execution.ExecResult{ExitCode: 2}}, Argv: []string{"build"}, Timeout: time.Minute}.
		BuildImage(context.Background(), req)
	if r, _ := RefusalOf(err); r == nil || r.err.Details["exit_code"] != "2" {
		t.Errorf("the exit status is not in the refusal: %v", err)
	}
}

func TestABuildThatCouldNotRunIsAnOutageNotTheCandidatesFault(t *testing.T) {
	t.Parallel()
	req := ImageRequest{RunID: "run-1", Unit: "app", Commit: baseSHA, Untrusted: true}
	interrupted, cancel := context.WithCancel(context.Background())
	defer cancel()
	for name, c := range map[string]struct {
		ctx  context.Context
		exec scriptedSandbox
		want core.ErrorCode
	}{
		"the sandbox cannot be created": {context.Background(),
			scriptedSandbox{createErr: core.NewError(core.CodeUnavailable, "docker is unreachable")}, core.CodeUnavailable},
		"the image is missing": {context.Background(),
			scriptedSandbox{createErr: core.NewError(core.CodeNotFound, "no such image")}, core.CodeNotFound},
		"the command could not be started": {context.Background(),
			scriptedSandbox{execErr: core.NewError(core.CodeInternal, "the container did not start")}, core.CodeInternal},
		"the supervisor is stopping": {interrupted,
			scriptedSandbox{result: execution.ExecResult{ExitCode: -1}, cancel: cancel}, core.CodeUnavailable},
	} {
		_, err := SandboxImages{Exec: c.exec, Argv: []string{"build"}, Timeout: time.Minute}.BuildImage(c.ctx, req)
		if _, refused := RefusalOf(err); refused || !core.HasCode(err, c.want) {
			t.Errorf("%s: %v, want an outage with %s", name, err, c.want)
		}
	}
	out, err := sandboxBuild(t, &phasedSandbox{phase: map[string]execution.ExecResult{}})
	if err != nil || out.Image != "registry.local/app@"+reportedDigest || !out.TestsPassed {
		t.Errorf("a successful build: %+v, %v", out, err)
	}
}

func TestAFailedBuildCommandNamesTheCommitItRanOn(t *testing.T) {
	t.Parallel()
	repo := &recordingRepo{files: map[string]string{"examples/hello-app/main.go": "a\n"}}
	b := newTestBuilder(repo)
	b.Images = SandboxImages{Exec: scriptedSandbox{result: execution.ExecResult{ExitCode: 1}}, Argv: []string{"build"}, Timeout: time.Minute}
	built, err := b.Build(context.Background(), BuildRequest{RunID: "run-1", Attempt: 2, Base: baseSHA,
		Diff: "--- a/examples/hello-app/main.go\n+++ b/examples/hello-app/main.go\n@@ -1 +1 @@\n-a\n+b\n"})
	if r, ok := RefusalOf(err); !ok || r.Reason != RefusalBuildFailed {
		t.Fatalf("a failing build command: %v", err)
	}
	if built.Branch != Branch("f1", "run-1", 2) || built.Record.Commit == "" || built.Record.Digest != "" || repo.commits != 1 {
		t.Errorf("the refusal names %+v after %d commits", built, repo.commits)
	}
}

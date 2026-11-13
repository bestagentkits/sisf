package stages

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// Without a program store the supervisor receives nothing of what a
// sandboxed build made: the report could name any image, the one already
// installed included, and the release would then watch, verify and merge
// on a program that does not hold the change. A sandboxed build is
// therefore never accepted on an image the supervisor did not observe. It
// is refused before any code runs, as the configuration's fault, never as
// the candidate's.
func TestAnImageTheSupervisorDidNotObserveIsNeverTakenAsTheBuildsResult(t *testing.T) {
	t.Parallel()
	s := &phasedSandbox{phase: map[string]execution.ExecResult{}}
	req := ImageRequest{RunID: "run-1", Unit: "app", Commit: baseSHA, Branch: "sisf/f1/run-1-1", Untrusted: true}
	out, err := SandboxImages{Exec: s, Argv: []string{"build"}, Timeout: time.Minute}.BuildImage(context.Background(), req)
	if err == nil {
		t.Fatalf("a build the supervisor observed nothing of was taken as %s", out.Image)
	}
	if _, refused := RefusalOf(err); refused {
		t.Errorf("the refusal is charged to the candidate: %v", err)
	}
	if !core.HasCode(err, core.CodeNotSupported) {
		t.Errorf("the refusal: %v, want NOT_SUPPORTED", err)
	}
	if len(s.envs) != 0 {
		t.Errorf("phases ran for a build that cannot be accepted: %v", s.envs)
	}
}

// The factory refuses at start to build candidates it could accept only on
// an image it did not observe, so no issue is ever taken up to be held.
func TestSandboxedBuildsWithoutAProgramStoreAreRefusedAtSelection(t *testing.T) {
	t.Parallel()
	_, err := SelectImageBuilder(context.Background(), ExecutionChoice{
		Profile: ProfileProduction, Agent: "claude", Docker: &phasedSandbox{},
		BuildArgv: []string{"build"}, BuildTimeout: time.Minute,
	})
	if !core.HasCode(err, core.CodeNotSupported) {
		t.Fatalf("a sandboxed builder without a program store: %v, want NOT_SUPPORTED", err)
	}
}

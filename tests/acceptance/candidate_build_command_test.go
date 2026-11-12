package acceptance

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// brokenGreetingDiff applies cleanly but leaves code that does not compile.
var brokenGreetingDiff = strings.Replace(greetingDiff, `println("hello, world") }`, `println("hello, world" }`, 1)

// buildSandbox stands in for an isolating execution backend. Its sandbox
// either cannot be created, or runs the build command to the given exit
// status; every command it ran is counted.
type buildSandbox struct {
	execution.Backend
	createErr error
	exitCode  int

	mu  sync.Mutex
	ran int
}

func (*buildSandbox) Name() string { return "docker" }

func (*buildSandbox) Capabilities(context.Context) (core.CapabilitySet, error) {
	return core.CapabilitySet{core.CapExecIsolation: core.SupportSupported, core.CapExecEgressEnforcement: core.SupportSupported}, nil
}

func (b *buildSandbox) CreateSandbox(context.Context, execution.SandboxSpec) (execution.SandboxInfo, error) {
	if b.createErr != nil {
		return execution.SandboxInfo{}, b.createErr
	}
	return execution.SandboxInfo{ID: "build-1", State: execution.SandboxRunning}, nil
}

func (b *buildSandbox) Exec(context.Context, execution.SandboxID, execution.Command) (execution.ExecResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ran++
	return execution.ExecResult{ExitCode: b.exitCode, Stderr: []byte("main.go:3: syntax error")}, nil
}

func (*buildSandbox) Destroy(context.Context, execution.SandboxID) error { return nil }

// firstBuildIn builds the first candidate in a sandbox and every later one
// with the harness's own image builder.
type firstBuildIn struct {
	sandbox stages.SandboxImages
	then    stages.ImageBuilder
	mu      sync.Mutex
	used    bool
}

func (b *firstBuildIn) Name() string { return b.then.Name() }

func (b *firstBuildIn) BuildImage(ctx context.Context, req stages.ImageRequest) (stages.ImageResult, error) {
	b.mu.Lock()
	first := !b.used
	b.used = true
	b.mu.Unlock()
	if first {
		return b.sandbox.BuildImage(ctx, req)
	}
	return b.then.BuildImage(ctx, req)
}

// A patch whose build command runs and fails in the sandbox, for example
// because it does not compile, is the patch's fault: it fails evaluation
// without being judged, spends its attempt, and the run's next attempt is
// built and released.
func TestAPatchThatDoesNotCompileFailsEvaluationAndIsRetried(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, brokenGreetingDiff)
	f.ScriptPatch(number, 0, greetingDiff)
	sandbox := &buildSandbox{exitCode: 1}
	f.Stages.Build.Builder.Images = &firstBuildIn{
		sandbox: stages.SandboxImages{Exec: sandbox, Argv: []string{"make", "image"}, Timeout: time.Minute},
		then:    f.Stages.Build.Builder.Images,
	}
	first := exportedPatch(t, f, number)

	f.Cycle(6)
	wantSurfaces(t, f, number, core.StateClosed)
	if sandbox.ran != 1 {
		t.Fatalf("the build command ran %d times in the sandbox, want once", sandbox.ran)
	}
	doc := candidateOf(t, f, first)
	if doc.BuildRefusal != string(stages.RefusalBuildFailed) || doc.Branch != stages.Branch(f.Manifest.FactoryID, first.RunID, first.Attempt) ||
		doc.Candidate.CommitSHA == "" || strings.Contains(doc.BuildFailure, "syntax error") {
		t.Fatalf("the failed build was recorded as %+v", doc)
	}
	journey := f.Journey(number)
	if i := slices.Index(journey, core.StateQAEvaluation); i < 0 || journey[i+1] != core.StateDeveloping {
		t.Fatalf("the patch that does not compile was not sent back to development: %v", journey)
	}
	if judged := f.Agent.Judged(f.Issue(number)); len(judged) != 1 {
		t.Fatalf("the patch that does not compile was judged: %d evaluations", len(judged))
	}
	if record := f.Record(number); record.Attempt != first.Attempt+1 {
		t.Fatalf("the issue closed at attempt %d, want %d", record.Attempt, first.Attempt+1)
	}
}

// A sandbox that cannot be created is an outage, not the patch's fault: the
// build is retried, no attempt is spent, and the build command never ran.
func TestASandboxThatCannotStartSpendsNoAttempt(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	record := exportedPatch(t, f, number)

	ctx := context.Background()
	build := f.Stages.Build
	working := build.Builder.Images
	sandbox := &buildSandbox{createErr: core.NewError(core.CodeUnavailable, "the Docker endpoint is unreachable")}
	build.Builder.Images = stages.SandboxImages{Exec: sandbox, Argv: []string{"make", "image"}, Timeout: time.Minute}
	build.Retry = stages.BuildRetry{First: time.Minute, Max: time.Minute, HoldAfter: 3}

	if res := build.Pass(ctx); len(res.Errors) == 0 || len(res.Unbuildable) != 0 || len(res.Submitted) != 0 {
		t.Fatalf("with no sandbox: %+v", res)
	}
	var outage stages.BuildOutage
	err := f.ControlStore.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		outage, err = stages.ReadBuildOutage(ctx, tx, record.RunID, record.Attempt)
		return err
	})
	if err != nil || outage.Failures != 1 || outage.LastCode != core.CodeUnavailable || sandbox.ran != 0 {
		t.Fatalf("the outage: %+v, %v, after %d commands", outage, err, sandbox.ran)
	}
	if r := f.Record(number); r.State != core.StateDeveloping || r.Attempt != record.Attempt {
		t.Fatalf("a sandbox that cannot start moved the issue to %s, attempt %d", r.State, r.Attempt)
	}

	build.Builder.Images = working
	f.Clock.Advance(time.Minute)
	if res := build.Pass(ctx); !slices.Equal(res.Submitted, []core.IssueRef{f.Issue(number)}) {
		t.Fatalf("once the sandbox starts: %+v", res)
	}
	if r := f.Record(number); r.Attempt != record.Attempt {
		t.Fatalf("the outage spent an attempt: %d, want %d", r.Attempt, record.Attempt)
	}
}

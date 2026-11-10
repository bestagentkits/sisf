package fake_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

const sha = core.CommitSHA("0123456789abcdef0123456789abcdef01234567")

var issue = core.IssueRef{Owner: "acme", Repo: "app", Number: 7}

func task(runID core.RunID, key string) agent.Task {
	return agent.Task{
		RunID: runID, IssueID: issue, Role: agent.RoleCoder, Step: core.StepCoding,
		Handoff: agent.Handoff{SoW: "Add GET /version.", DoD: "GET /version returns the build revision."},
		Policy: core.PolicyRefs{PolicySHA: sha, ReviewSHA: sha, DecisionSHA: sha, FeatureCatalogSHA: sha,
			ModelRoute: core.ModelRoute{Provider: "anthropic", Model: "mid"}},
		Limits:         agent.Limits{Timeout: time.Hour},
		IdempotencyKey: key,
	}
}

func newBackend(t *testing.T, cfg fake.Config) (*fake.Backend, *sqlstoretest.Clock) {
	t.Helper()
	clock := sqlstoretest.NewClock(time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC))
	cfg.Clock = clock
	b, err := fake.New(cfg)
	if err != nil {
		t.Fatalf("new backend: %v", err)
	}
	return b, clock
}

func start(t *testing.T, b *fake.Backend, tk agent.Task) agent.RunRef {
	t.Helper()
	ref, err := b.Start(context.Background(), tk, agent.WorkspaceRef{ID: "ws"}, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return ref
}

func phase(t *testing.T, b *fake.Backend, ref agent.RunRef) agent.Phase {
	t.Helper()
	status, err := b.Status(context.Background(), ref)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	return status.Phase
}

func TestAScriptedPatchSucceedsOnTheClockAndLeavesItsPatch(t *testing.T) {
	t.Parallel()
	b, clock := newBackend(t, fake.Config{})
	diff := "+hello\n"
	if err := b.Script(agent.RoleCoder, issue, fake.Patch(time.Minute, diff)); err != nil {
		t.Fatal(err)
	}
	ref := start(t, b, task("run-1", "k1"))
	if got := phase(t, b, ref); got != agent.PhaseRunning {
		t.Fatalf("before its time the run is %s, want running", got)
	}
	if _, err := b.CollectArtifacts(context.Background(), ref); !core.HasCode(err, core.CodeConflict) {
		t.Fatalf("a running run handed over artifacts: %v", err)
	}
	clock.Advance(time.Minute)
	if got := phase(t, b, ref); got != agent.PhaseSucceeded {
		t.Fatalf("after its time the run is %s, want succeeded", got)
	}
	artifacts, err := b.CollectArtifacts(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(diff))
	if len(artifacts) != 1 || artifacts[0].Kind != agent.ArtifactPatch ||
		artifacts[0].Digest != core.Digest("sha256:"+hex.EncodeToString(sum[:])) || artifacts[0].Digest.Validate() != nil {
		t.Fatalf("unexpected artifacts %+v", artifacts)
	}
	page, err := b.Events(context.Background(), ref, "")
	if err != nil || len(page.Events) != 2 {
		t.Fatalf("want a start and a finish event, got %+v, %v", page, err)
	}
	rest, err := b.Events(context.Background(), ref, page.Next)
	if err != nil || len(rest.Events) != 0 {
		t.Fatalf("reading from the last cursor returned %+v, %v", rest, err)
	}
}

func TestScriptedFailureHangAndCrash(t *testing.T) {
	t.Parallel()
	b, clock := newBackend(t, fake.Config{})
	if err := b.Script(agent.RoleCoder, issue, fake.Fail(0, "tests failed"), fake.Hang(), fake.Crash(time.Second)); err != nil {
		t.Fatal(err)
	}
	failed := start(t, b, task("run-1", "k1"))
	hung := start(t, b, task("run-2", "k2"))
	crashed := start(t, b, task("run-3", "k3"))
	clock.Advance(24 * time.Hour)

	if got := phase(t, b, failed); got != agent.PhaseFailed {
		t.Errorf("failed run is %s", got)
	}
	if got := phase(t, b, hung); got != agent.PhaseRunning {
		t.Errorf("hanging run is %s, want still running", got)
	}
	if _, err := b.Status(context.Background(), crashed); !core.HasCode(err, core.CodeUnavailable) {
		t.Errorf("a crashed run must answer UNAVAILABLE, got %v", err)
	}
	if err := b.Cancel(context.Background(), hung); err != nil {
		t.Fatal(err)
	}
	if got := phase(t, b, hung); got != agent.PhaseCancelled {
		t.Errorf("cancelled run is %s", got)
	}
	if err := b.Cancel(context.Background(), failed); err != nil || phase(t, b, failed) != agent.PhaseFailed {
		t.Errorf("cancelling a finished run changed it: %v", err)
	}
}

func TestARepeatedStartReturnsTheSameRun(t *testing.T) {
	t.Parallel()
	b, _ := newBackend(t, fake.Config{})
	first := start(t, b, task("run-1", "k1"))
	again := start(t, b, task("run-1", "k1"))
	if first != again || len(b.Started(issue)) != 1 {
		t.Fatalf("a repeated start began another run: %+v %+v, %d started", first, again, len(b.Started(issue)))
	}
	if _, err := b.Start(context.Background(), task("run-2", "k1"), agent.WorkspaceRef{}, nil); !core.HasCode(err, core.CodeConflict) {
		t.Fatalf("an idempotency key reused for another run must conflict, got %v", err)
	}
}

func TestTheBackendRefusesWhatItCannotDo(t *testing.T) {
	t.Parallel()
	b, _ := newBackend(t, fake.Config{})
	qa := task("run-1", "k1")
	qa.Role, qa.Step = agent.RoleQA, core.StepQA
	if _, err := b.Start(context.Background(), qa, agent.WorkspaceRef{}, nil); !core.HasCode(err, core.CodeNotSupported) {
		t.Errorf("a coder-only backend must refuse the evaluator role, got %v", err)
	}
	invalid := task("run-1", "k1")
	invalid.Limits.Timeout = 0
	if _, err := b.Start(context.Background(), invalid, agent.WorkspaceRef{}, nil); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a task without a time limit must be refused, got %v", err)
	}
	if _, err := b.Status(context.Background(), agent.RunRef{Backend: "fake", ID: "fake-9", RunID: "run-9"}); !core.HasCode(err, core.CodeNotFound) {
		t.Errorf("an unknown run must be NOT_FOUND, got %v", err)
	}
	if err := b.Script(agent.RoleCoder, issue, fake.Step{Outcome: "explode"}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("an unknown outcome must be refused, got %v", err)
	}
	if _, err := fake.New(fake.Config{Roles: []agent.Role{"owner"}}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("an unknown role must be refused, got %v", err)
	}
	caps, err := b.Capabilities(context.Background())
	if err != nil || !caps.Supports(core.CapAgentCancel) || caps.Supports(core.CapAgentHardBudget) {
		t.Errorf("capabilities %+v, %v: want cancel only", caps, err)
	}
}

func TestAnEvaluatorRoleIsScriptedApartFromTheCoder(t *testing.T) {
	t.Parallel()
	b, _ := newBackend(t, fake.Config{Roles: []agent.Role{agent.RoleCoder, agent.RoleQA}})
	if err := b.Script(agent.RoleQA, issue, fake.Fail(0, "score below threshold")); err != nil {
		t.Fatal(err)
	}
	coder := start(t, b, task("run-1", "k1"))
	qa := task("run-1", "k2")
	qa.Role, qa.Step = agent.RoleQA, core.StepQA
	evaluator := start(t, b, qa)
	if got := phase(t, b, coder); got != agent.PhaseSucceeded {
		t.Errorf("the coder followed the evaluator's script: %s", got)
	}
	if got := phase(t, b, evaluator); got != agent.PhaseFailed {
		t.Errorf("the evaluator did not follow its script: %s", got)
	}
}

package acceptance

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/lifecycle"
	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	iqa "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
)

// pushWorkflowDiff adds a workflow that would run on every branch push.
const pushWorkflowDiff = `--- /dev/null
+++ b/.github/workflows/exfiltrate.yml
@@ -0,0 +1,4 @@
+on: push
+jobs:
+  x:
+    runs-on: ubuntu-latest
`

// exportedPatch drives the factory until the agent zone has exported the
// patch of issue number's current attempt, and returns the record.
func exportedPatch(t *testing.T, f *harness.Factory, number int) core.StateRecord {
	t.Helper()
	ctx := context.Background()
	for range 6 {
		f.Pass()
		f.Tick()
		if res := f.Exporter.Pass(ctx); len(res.Errors) > 0 {
			t.Fatalf("exporter: %v", res.Errors)
		}
		record := f.Record(number)
		if record.State != core.StateDeveloping {
			continue
		}
		err := f.ControlStore.View(ctx, func(ctx context.Context, tx state.Tx) error {
			_, err := handoff.ReadPatch(ctx, tx, record.RunID, record.Attempt)
			return err
		})
		if err == nil {
			return record
		}
	}
	t.Fatalf("issue %d left no exported patch", number)
	return core.StateRecord{}
}

func candidateOf(t *testing.T, f *harness.Factory, record core.StateRecord) handoff.CandidateDoc {
	t.Helper()
	var doc handoff.CandidateDoc
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		doc, err = handoff.ReadCandidate(ctx, tx, record.RunID, record.Attempt)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// rewrite changes a document of the agent zone, as a compromised agent zone could.
func rewrite[T any](t *testing.T, f *harness.Factory, key string, change func(*T)) {
	t.Helper()
	err := f.AgentStore.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, handoff.Namespace, key)
		if err != nil {
			return err
		}
		var doc T
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			return err
		}
		change(&doc)
		entry.Value, err = json.Marshal(doc)
		if err != nil {
			return err
		}
		_, err = tx.PutKV(ctx, entry)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A coding agent's patch that adds a push-triggered workflow is refused
// before the supervisor commits or pushes anything with its GitHub token:
// GitHub receives no git data write, the refusal is recorded as a failed
// build of the patch with its reason, and the run's next attempt is built
// and released.
func TestAPatchTouchingCIIsRefusedBeforeAnythingIsPushed(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff+pushWorkflowDiff)
	f.ScriptPatch(number, 0, greetingDiff)

	record := exportedPatch(t, f, number)
	writes := f.GitData.Writes()
	res := f.Stages.Build.Pass(context.Background())
	if len(res.Errors) > 0 || !slices.Equal(res.Unbuildable, []core.IssueRef{f.Issue(number)}) {
		t.Fatalf("the build pass: %+v", res)
	}
	if got := f.GitData.Writes(); got != writes {
		t.Fatalf("GitHub received %d git data writes for a refused patch", got-writes)
	}
	doc := candidateOf(t, f, record)
	if doc.BuildRefusal != string(stages.RefusalProtectedPath) || !strings.Contains(doc.BuildFailure, ".github/workflows/exfiltrate.yml") {
		t.Fatalf("the refusal was recorded as %q: %q", doc.BuildRefusal, doc.BuildFailure)
	}

	f.Cycle(6)
	wantSurfaces(t, f, number, core.StateClosed)
	if r := f.Record(number); r.Attempt != 2 {
		t.Fatalf("the issue closed at attempt %d, want 2", r.Attempt)
	}
	for _, b := range f.GitData.Branches(f.Repo) {
		if b == stages.Branch(f.Manifest.FactoryID, record.RunID, 1) {
			t.Fatalf("the refused patch reached branch %s", b)
		}
		if _, files, _ := f.GitData.Branch(f.Repo, b); files[".github/workflows/exfiltrate.yml"] != "" {
			t.Fatalf("branch %s holds the agent's workflow", b)
		}
	}
}

// The coder named on the candidate, and so on the signed receipt, comes
// from the run's dispatch record, not from the patch document the agent
// zone wrote; and the branch carries the state record's attempt, not the
// one the patch document claims.
func TestTheCoderAndTheAttemptComeFromTrustedRecords(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)

	record := exportedPatch(t, f, number)
	rewrite(t, f, handoff.PatchKey(record.RunID, record.Attempt), func(p *handoff.PatchDoc) {
		p.CoderIdentity = "agent:nobody/coder"
	})
	if res := f.Stages.Build.Pass(context.Background()); len(res.Submitted) != 1 {
		t.Fatalf("the build pass: %+v", res)
	}
	doc := candidateOf(t, f, record)
	if doc.Candidate.CodingAgentIdentity != lifecycle.CoderIdentity("fake") {
		t.Fatalf("the candidate names the coder %q", doc.Candidate.CodingAgentIdentity)
	}
	if doc.Branch != stages.Branch(f.Manifest.FactoryID, record.RunID, record.Attempt) {
		t.Fatalf("the candidate went to branch %s", doc.Branch)
	}
}

// A patch document that names another agent attempt than the dispatch
// record cannot pick its branch, and its coder is unverified: the receipt
// says so and the independent-evaluator gate fails it, so it is never
// released.
func TestAnUnverifiedCoderFailsTheIndependenceGate(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)

	record := exportedPatch(t, f, number)
	rewrite(t, f, handoff.PatchKey(record.RunID, record.Attempt), func(p *handoff.PatchDoc) { p.AgentAttempt = 7 })
	ctx := context.Background()
	if res := f.Stages.Build.Pass(ctx); len(res.Submitted) != 1 {
		t.Fatalf("the build pass: %+v", res)
	}
	doc := candidateOf(t, f, record)
	if doc.Candidate.CodingAgentIdentity != iqa.UnverifiedCodingAgent || doc.Branch != stages.Branch(f.Manifest.FactoryID, record.RunID, 1) {
		t.Fatalf("the candidate names the coder %q on branch %s", doc.Candidate.CodingAgentIdentity, doc.Branch)
	}
	if res := f.Stages.QA.Pass(ctx); len(res.Errors) > 0 {
		t.Fatalf("the QA pass: %+v", res.Errors)
	}
	var result stages.QAResult
	err := f.ControlStore.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		result, err = stages.ReadQAResult(ctx, tx, record.RunID, record.Attempt)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.CodingAgentIdentity != iqa.UnverifiedCodingAgent {
		t.Fatalf("the receipt names the coder %q", result.Receipt.CodingAgentIdentity)
	}
	failed := slices.ContainsFunc(result.Receipt.Checks, func(c core.QACheck) bool {
		return c.Name == string(qa.GateIndependentEvaluator) && !c.Passed
	})
	if !failed || f.Record(number).State == core.StateReleaseCandidate {
		t.Fatalf("the unverified coder passed independence: %+v", result)
	}
}

// failingCommits fails the next commits with a GitHub error and records
// every commit asked for.
type failingCommits struct {
	stages.Repository
	fail    int
	commits int
}

func (r *failingCommits) CommitToBranch(ctx context.Context, repo github.RepoRef, bc github.BranchCommit) (core.CommitSHA, error) {
	r.commits++
	if r.fail > 0 {
		r.fail--
		return "", core.NewError(core.CodeConflict, "GitHub answered 409")
	}
	return r.Repository.CommitToBranch(ctx, repo, bc)
}

// A GitHub error from the commit is an outage, not the patch's fault: it
// spends no attempt, is retried after a bounded backoff, is reported held
// after repeated failures, and the patch is built once GitHub recovers.
func TestAGitHubOutageSpendsNoAttemptAndIsRetried(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	record := exportedPatch(t, f, number)

	ctx := context.Background()
	build := f.Stages.Build
	git := &failingCommits{Repository: build.Builder.Git, fail: 2}
	build.Builder.Git = git
	build.Retry = stages.BuildRetry{First: time.Minute, Max: 4 * time.Minute, HoldAfter: 2}

	if res := build.Pass(ctx); len(res.Errors) == 0 || len(res.Unbuildable) != 0 || len(res.Held) != 0 {
		t.Fatalf("the first outage: %+v", res)
	}
	if res := build.Pass(ctx); !slices.Equal(res.Deferred, []core.IssueRef{f.Issue(number)}) || git.commits != 1 {
		t.Fatalf("within the backoff: %+v after %d commits", res, git.commits)
	}
	f.Clock.Advance(time.Minute)
	if res := build.Pass(ctx); len(res.Errors) == 0 || !slices.Equal(res.Held, []core.IssueRef{f.Issue(number)}) {
		t.Fatalf("the second outage: %+v", res)
	}
	var outage stages.BuildOutage
	err := f.ControlStore.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		outage, err = stages.ReadBuildOutage(ctx, tx, record.RunID, record.Attempt)
		return err
	})
	if err != nil || outage.Failures != 2 || !outage.Held || outage.LastCode != core.CodeConflict {
		t.Fatalf("the outage record: %+v, %v", outage, err)
	}
	if r := f.Record(number); r.State != core.StateDeveloping || r.Attempt != record.Attempt {
		t.Fatalf("the outage moved the issue to %s, attempt %d", r.State, r.Attempt)
	}

	f.Clock.Advance(2 * time.Minute)
	if res := build.Pass(ctx); len(res.Submitted) != 1 || len(res.Errors) != 0 {
		t.Fatalf("after GitHub recovered: %+v", res)
	}
	err = f.ControlStore.View(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := stages.ReadBuildOutage(ctx, tx, record.RunID, record.Attempt)
		return err
	})
	if !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("the outage record outlived the build: %v", err)
	}
	if runs := f.Runs(number); len(runs) != 1 || runs[0].Attempt != 1 || runs[0].Status != dispatcher.RunSucceeded {
		t.Fatalf("the outage spent a coding attempt: %+v", runs)
	}
}

package acceptance

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// putPolicyInForce commits the foundation files once more with note added
// to the decision rules and puts that revision in force as the Owner does:
// request, approval, then the change itself.
func putPolicyInForce(t *testing.T, f *harness.Factory, note string) core.CommitSHA {
	t.Helper()
	// The application's sources come along, so a patch can be built on the
	// new revision as on the first.
	files := maps.Clone(harness.SeedFiles)
	for _, path := range []string{policy.PathFactory, policy.PathAgents, policy.PathReview, policy.PathDecisions, policy.PathFeatures} {
		content, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		files[path] = string(content)
	}
	files[policy.PathDecisions] = strings.TrimRight(files[policy.PathDecisions], "\n") + "\n\n" + note + "\n"
	sha, err := f.GitHub.Commit(f.Repo, files)
	if err != nil {
		t.Fatal(err)
	}
	next := core.CommitSHA(sha)
	approveRevision(t, f, next)
	return next
}

// approveRevision puts revision in force as the Owner does: request,
// approval, then the change itself.
func approveRevision(t *testing.T, f *harness.Factory, next core.CommitSHA) {
	t.Helper()
	ctx := context.Background()
	request, err := f.Control.RequestPolicyChange(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	owner := core.Actor{ID: "owner:local", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceCLI,
		StepUpAt: f.Clock.Now()}
	if _, err := f.Control.Approvals.Submit(ctx, owner, approvals.Submission{RequestID: request.ID, Nonce: request.Nonce,
		SubjectDigest: request.SubjectDigest, PolicyRevision: request.PolicyRevision, Decision: approvals.DecisionApprove}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Control.ApproveBaseline(ctx, next, request.ID); err != nil {
		t.Fatal(err)
	}
}

// A run is coded against the policy its scope was written under. When the
// Owner puts another policy in force while the run is under way, its patch
// is neither built nor judged on the new policy, so never released on it:
// the build is held at once, spends no attempt, and the Owner is told why
// on the issue. The run has no way on, so the issue is blocked; the
// Owner's retry has its scope judged under the policy in force and returns
// it to Ready, where a new run codes it under that policy.
func TestAPatchWrittenUnderASupersededPolicyIsNotBuiltOnTheNewOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	record := exportedPatch(t, f, number)
	inForce := putPolicyInForce(t, f, "A rule added while the run was under way.")

	res := f.Stages.Build.Pass(ctx)
	if !slices.Equal(res.Held, []core.IssueRef{f.Issue(number)}) || len(res.Submitted) != 0 || len(res.Unbuildable) != 0 {
		t.Fatalf("the build of a patch written under a superseded policy was not held at once: %+v", res)
	}
	if after := f.Record(number); after.State != core.StateDeveloping || after.Attempt != record.Attempt {
		t.Fatalf("the issue is %s at attempt %d, want Developing at %d", after.State, after.Attempt, record.Attempt)
	}
	noCandidate := func(when string) {
		t.Helper()
		err := f.ControlStore.View(ctx, func(ctx context.Context, tx state.Tx) error {
			_, err := handoff.ReadCandidate(ctx, tx, record.RunID, record.Attempt)
			return err
		})
		if !core.HasCode(err, core.CodeNotFound) {
			t.Fatalf("%s a candidate of the superseded run was built on the new policy: %v", when, err)
		}
	}
	noCandidate("at the hold")
	if held := heldListing(t, f); len(held) != 1 || held[0].Reason != handoff.HeldPolicySuperseded {
		t.Fatalf("the held build does not say the policy was superseded: %+v", held)
	}

	f.Pass()
	if got := countComments(t, f, number, "policy in force changed"); got != 1 {
		t.Fatalf("the Owner was told %d times that the policy changed under the run", got)
	}
	// The run has no way on: its issue is blocked, with the cause on record
	// and in the progress comment, and nothing of it waits as a held build.
	wantSurfaces(t, f, number, core.StateBlocked)
	wantLabels(t, f, number, github.LabelBlocked)
	if stop := runStopOf(t, f, record.RunID); stop.Cause != statemachine.BlockPolicySuperseded {
		t.Fatalf("the record of the stopped run: %+v", stop)
	}
	if held := heldListing(t, f); len(held) != 0 {
		t.Fatalf("the blocked issue still has a held build: %+v", held)
	}
	retry := "factoryd issue retry --issue " + f.Issue(number).String()
	if c := commentContaining(t, f, number, "<!-- sisf:progress "); !strings.Contains(c, "**Blocked:**") || !strings.Contains(c, retry) {
		t.Errorf("the progress comment does not say why the issue is blocked:\n%s", c)
	}
	if why := f.Explain(number); !why.Stopped || !strings.Contains(why.Reason, "no longer in force") || !strings.Contains(why.Reason, retry) {
		t.Errorf("runs.explain answers %+v", why)
	}
	// The block stands: later passes neither build it nor tell the Owner again.
	f.Cycle(2)
	if got := countComments(t, f, number, "policy in force changed"); got != 1 {
		t.Fatalf("the Owner was told %d times", got)
	}
	if got := f.Record(number); got.State != core.StateBlocked {
		t.Fatalf("the blocked issue moved by itself to %s", got.State)
	}
	noCandidate("after the block")
	// The run is over, so the budget it reserved is no longer held.
	if _, reservation := ledgerOf(t, f, f.Runs(number)[0].ReservationID); reservation.Status == core.ReservationHeld {
		t.Fatalf("the reservation of the superseded run is still held: %+v", reservation)
	}

	// Only the Owner retries it. The scope lies inside what the policy in
	// force delegates, so it is pinned to that policy and the issue is
	// Ready again.
	maintainer := core.Actor{ID: "user:maintainer", Kind: core.ActorHuman, Roles: []core.Role{core.RoleMaintainer}, Surface: core.SurfaceCLI}
	if _, err := f.Control.Retry(ctx, f.Issue(number), maintainer); !core.HasCode(err, core.CodeGuardFailed) {
		t.Fatalf("a maintainer retried the blocked issue: %v", err)
	}
	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's retry: %v", err)
	}
	if scope, err := f.Control.Intake.ApprovedScope(ctx, f.Issue(number)); err != nil || scope.PolicyRevision != inForce {
		t.Fatalf("the retried scope is pinned to %s, want the policy in force %s: %v", scope.PolicyRevision, inForce, err)
	}
	if got := f.Record(number).State; got != core.StateReady {
		t.Fatalf("the retried issue is %s, want Ready", got)
	}

	// A new run codes it under the policy in force, and its candidate is
	// built on that policy; nothing of the superseded run ever was.
	f.ScriptPatch(number, 0, greetingDiff)
	again := exportedPatch(t, f, number)
	if again.RunID == record.RunID {
		t.Fatal("the retried issue was not claimed by a new run")
	}
	if tasks := f.Agent.Started(f.Issue(number)); len(tasks) != 2 || tasks[1].Policy.PolicySHA != inForce {
		t.Fatalf("the new run was not coded under the policy in force: %+v", tasks)
	}
	if res := f.Stages.Build.Pass(ctx); !slices.Equal(res.Submitted, []core.IssueRef{f.Issue(number)}) {
		t.Fatalf("the build of the new run on the policy in force: %+v", res)
	}
	if doc := candidateOf(t, f, again); doc.Candidate.BaselineSHA != inForce {
		t.Fatalf("the candidate was built on %s, want the policy in force %s", doc.Candidate.BaselineSHA, inForce)
	}
	noCandidate("in the end")
}

// A scope the Owner had to approve was approved under the policy then in
// force. When its run is blocked because that policy was superseded, the
// Owner's retry does not carry the old approval over: the Owner is asked to
// approve the scope under the policy in force, and only that approval
// returns the issue to Ready.
func TestARetryAfterASupersededPolicyNeedsTheScopeApprovedUnderThePolicyInForce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("visitor", "NONE", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.Pass()
	if got := f.Record(number).State; got != core.StateApprovalPending {
		t.Fatalf("the outsider's issue is %s, want ApprovalPending, the premise of this test", got)
	}
	approvePending(t, f, number)
	f.Pass()
	f.ScriptPatch(number, 0, greetingDiff)
	record := exportedPatch(t, f, number)
	inForce := putPolicyInForce(t, f, "A rule added while the run was under way.")
	f.Stages.Build.Pass(ctx)
	f.Pass()
	wantSurfaces(t, f, number, core.StateBlocked)

	_, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f))
	if !core.HasCode(err, core.CodeApprovalRequired) {
		t.Fatalf("the retry carried the approval of the superseded policy over: %v", err)
	}
	if got := f.Record(number); got.State != core.StateBlocked || got.RunID != record.RunID {
		t.Fatalf("the refused retry moved the issue: %+v", got)
	}
	// An approval counts only when it was given after the block.
	f.Clock.Advance(time.Minute)
	approvePending(t, f, number)
	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's retry after approving the scope under the policy in force: %v", err)
	}
	if scope, err := f.Control.Intake.ApprovedScope(ctx, f.Issue(number)); err != nil || scope.PolicyRevision != inForce {
		t.Fatalf("the retried scope is pinned to %s, want %s: %v", scope.PolicyRevision, inForce, err)
	}
	if got := f.Record(number).State; got != core.StateReady {
		t.Fatalf("the retried issue is %s, want Ready", got)
	}
}

// approvePending approves, as the Owner, the one pending request to
// approve the scope of issue number.
func approvePending(t *testing.T, f *harness.Factory, number int) {
	t.Helper()
	ctx := context.Background()
	pending, err := f.Control.Approvals.List(ctx, approvals.Filter{IssueID: f.Issue(number), Statuses: []approvals.Status{approvals.StatusPending}})
	if err != nil || len(pending) != 1 {
		t.Fatalf("the issue has %d pending approval requests, want 1: %v", len(pending), err)
	}
	request := pending[0]
	if _, err := f.Control.Approvals.Submit(ctx, ownerActor(f), approvals.Submission{RequestID: request.ID, Nonce: request.Nonce,
		SubjectDigest: request.SubjectDigest, PolicyRevision: request.PolicyRevision, Decision: approvals.DecisionApprove}); err != nil {
		t.Fatal(err)
	}
}

// A patch pinned to another policy than the one in force is not built on
// it either, even when the scope names the policy in force, as after the
// scope was approved again during the run.
func TestAPatchPinnedToAnotherPolicyIsNotBuiltOnTheOneInForce(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	record := exportedPatch(t, f, number)
	rewrite(t, f, handoff.PatchKey(record.RunID, record.Attempt), func(p *handoff.PatchDoc) {
		p.Policy.PolicySHA = core.CommitSHA(strings.Repeat("e", 40))
	})
	res := f.Stages.Build.Pass(context.Background())
	if !slices.Equal(res.Held, []core.IssueRef{f.Issue(number)}) || len(res.Submitted) != 0 || len(res.Unbuildable) != 0 {
		t.Fatalf("a patch pinned to another policy was not held: %+v", res)
	}
}

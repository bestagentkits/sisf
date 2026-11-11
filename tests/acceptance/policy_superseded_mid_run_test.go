package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// putPolicyInForce commits the foundation files once more with note added
// to the decision rules and puts that revision in force as the Owner does:
// request, approval, then the change itself.
func putPolicyInForce(t *testing.T, f *harness.Factory, note string) core.CommitSHA {
	t.Helper()
	files := map[string]string{}
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
// on the issue.
func TestAPatchWrittenUnderASupersededPolicyIsNotBuiltOnTheNewOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	record := exportedPatch(t, f, number)
	putPolicyInForce(t, f, "A rule added while the run was under way.")

	res := f.Stages.Build.Pass(ctx)
	if !slices.Equal(res.Held, []core.IssueRef{f.Issue(number)}) || len(res.Submitted) != 0 || len(res.Unbuildable) != 0 {
		t.Fatalf("the build of a patch written under a superseded policy was not held at once: %+v", res)
	}
	if after := f.Record(number); after.State != core.StateDeveloping || after.Attempt != record.Attempt {
		t.Fatalf("the issue is %s at attempt %d, want Developing at %d", after.State, after.Attempt, record.Attempt)
	}
	err := f.ControlStore.View(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := handoff.ReadCandidate(ctx, tx, record.RunID, record.Attempt)
		return err
	})
	if !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("a candidate was built on the new policy: %v", err)
	}
	if held := heldListing(t, f); len(held) != 1 || held[0].Reason != handoff.HeldPolicySuperseded {
		t.Fatalf("the held build does not say the policy was superseded: %+v", held)
	}

	f.Pass()
	if got := countComments(t, f, number, "policy in force changed"); got != 1 {
		t.Fatalf("the Owner was told %d times that the policy changed under the run", got)
	}
	// The hold stands: later passes neither build it nor tell the Owner again.
	f.Stages.Build.Pass(ctx)
	f.Pass()
	if got := countComments(t, f, number, "policy in force changed"); got != 1 {
		t.Fatalf("the Owner was told %d times", got)
	}
	// The Owner puts the run's policy back in force and retries: the patch
	// is built on the policy it was written under.
	approveRevision(t, f, f.PolicyRevision)
	if _, err := f.Control.Retry(ctx, f.Issue(number), core.Actor{ID: "owner:local", Kind: core.ActorHuman,
		Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceCLI, StepUpAt: f.Clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if res := f.Stages.Build.Pass(ctx); !slices.Equal(res.Submitted, []core.IssueRef{f.Issue(number)}) {
		t.Fatalf("the resumed build on the run's own policy: %+v", res)
	}
	if doc := candidateOf(t, f, record); doc.Candidate.BaselineSHA != f.PolicyRevision {
		t.Fatalf("the candidate was built on %s, want the run's policy %s", doc.Candidate.BaselineSHA, f.PolicyRevision)
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

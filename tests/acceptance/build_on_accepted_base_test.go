package acceptance

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// acceptingOnly is a keeper of the commit to build on that accepts the
// approved revision and one followed merge, and nothing else.
type acceptingOnly struct{ revision, merge core.CommitSHA }

func (k acceptingOnly) Accept(_ context.Context, base core.CommitSHA) (core.CommitSHA, error) {
	if base == k.revision || base == k.merge {
		return base, nil
	}
	return "", core.NewError(core.CodePolicyHold, "not a commit the factory builds on")
}

func (acceptingOnly) CheckMain(context.Context, core.Actor, policy.MainCheck) error { return nil }

func (acceptingOnly) Follow(context.Context, core.Actor, policy.FollowProof) (policy.BuildBase, error) {
	return policy.BuildBase{}, nil
}

// A run coded on a merge the supervisor followed is built and judged on
// that merge, the commit the keeper accepted, not on the policy revision:
// building it on the revision would leave out the release before it.
func TestARunIsBuiltOnTheCommitItWasCodedOn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	revision := policyRevisionInForce(t, f)

	first := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(first, 0, greetingDiff)
	f.Cycle(4)
	wantSurfaces(t, f, first, core.StateClosed)
	head, _, ok := f.GitData.Branch(f.Repo, "main")
	merge := core.CommitSHA(head)
	if !ok || merge == revision {
		t.Fatalf("the first release left main at %q, the policy revision is %s", head, revision)
	}

	second := f.OpenIssue("maintainer", "MEMBER", "Show the status", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(second, 0, dashboardDiff)
	record := exportedPatch(t, f, second)
	// The run was pinned to the merge the supervisor followed.
	rewrite(t, f, handoff.PatchKey(record.RunID, record.Attempt), func(p *handoff.PatchDoc) { p.Base = merge })
	f.Stages.Build.Bases = acceptingOnly{revision: revision, merge: merge}

	if res := f.Stages.Build.Pass(ctx); !slices.Equal(res.Submitted, []core.IssueRef{f.Issue(second)}) {
		t.Fatalf("the run coded on the followed merge was not built: %+v", res)
	}
	if got := candidateOf(t, f, record).Candidate.BaselineSHA; got != merge {
		t.Fatalf("the candidate was built and judged on %s, want the commit it was coded on, %s", got, merge)
	}
}

// refusingAll is the keeper of a factory that stopped building on main: it
// accepts no base, the policy revision included.
type refusingAll struct{ acceptingOnly }

func (refusingAll) Accept(context.Context, core.CommitSHA) (core.CommitSHA, error) {
	return "", core.NewError(core.CodePolicyHold, "the factory stopped building on main")
}

// A run whose base the keeper refuses is not built, whatever commit its
// patch names: it ends as superseded, blocked for the Owner, and nothing is
// sent to QA.
func TestARunOnABaseTheKeeperRefusesIsNotBuilt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	record := exportedPatch(t, f, number)
	f.Stages.Build.Bases = refusingAll{}

	res := f.Stages.Build.Pass(ctx)
	if len(res.Submitted) != 0 || len(res.Unbuildable) != 0 || !strings.Contains(fmt.Sprint(res.Errors), "stopped building on main") {
		t.Fatalf("a run on a refused base was built: %+v", res)
	}
	err := f.ControlStore.View(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := handoff.ReadCandidate(ctx, tx, record.RunID, record.Attempt)
		return err
	})
	if !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("a run on a refused base was sent to QA: %v", err)
	}
	f.Pass()
	if stop := runStopOf(t, f, record.RunID); stop.Cause != statemachine.BlockPolicySuperseded {
		t.Fatalf("the run on a refused base ended %+v, want it superseded", stop)
	}
}

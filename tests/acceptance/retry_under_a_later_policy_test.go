package acceptance

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/github"
)

// The Owner often puts another policy in force because an issue is blocked,
// for example to move the baseline after a release was refused for lacking
// the last known good release. The retry of an issue whose run stopped for
// any reason then has its scope judged under the policy in force and pinned
// to it, so the run that claims it next is coded under that policy and is
// not stopped as superseded after it was coded.
func TestARetryAfterTheOwnerChangedThePolicyPinsTheScopeToThePolicyInForce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	failedAttempts(t, f, number)
	f.Cycle(6)
	wantSurfaces(t, f, number, core.StateBlocked)
	if stop := runStopOf(t, f, f.Record(number).RunID); stop.Cause != statemachine.BlockRunExhausted {
		t.Fatalf("the run stopped with cause %s, want it exhausted, the premise of this test", stop.Cause)
	}
	inForce := putPolicyInForce(t, f, "A rule added after the issue was blocked.")

	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's retry under the policy in force: %v", err)
	}
	if got := f.Record(number).State; got != core.StateReady {
		t.Fatalf("the retried issue is %s, want Ready", got)
	}
	if scope, err := f.Control.Intake.ApprovedScope(ctx, f.Issue(number)); err != nil || scope.PolicyRevision != inForce {
		t.Fatalf("the retried scope is pinned to %s, want the policy in force %s: %v", scope.PolicyRevision, inForce, err)
	}
}

// A scope the Owner approved was approved under the policy then in force.
// Once another policy is in force, the retry of its blocked issue does not
// carry that approval over, whatever stopped the run: the Owner is asked to
// approve the scope under the policy in force first.
func TestARetryAfterTheOwnerChangedThePolicyNeedsAnApprovedScopeApprovedAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("visitor", "NONE", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.Pass()
	if got := f.Record(number).State; got != core.StateApprovalPending {
		t.Fatalf("the outsider's issue is %s, want ApprovalPending, the premise of this test", got)
	}
	approvePending(t, f, number)
	failedAttempts(t, f, number)
	f.Cycle(6)
	wantSurfaces(t, f, number, core.StateBlocked)
	blocked := f.Record(number)
	inForce := putPolicyInForce(t, f, "A rule added after the issue was blocked.")

	_, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f))
	if !core.HasCode(err, core.CodeApprovalRequired) {
		t.Fatalf("the retry carried the approval of the earlier policy over: %v", err)
	}
	if got := f.Record(number); got.State != core.StateBlocked || got.RunID != blocked.RunID {
		t.Fatalf("the refused retry moved the issue: %+v", got)
	}
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

// While the policy the scope was approved under is still in force, the
// retry of a stopped run needs nothing but the Owner asking: the approval
// stands, and the scope is neither judged nor written again.
func TestARetryUnderTheSamePolicyKeepsTheApprovedScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("visitor", "NONE", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.Pass()
	approvePending(t, f, number)
	failedAttempts(t, f, number)
	f.Cycle(6)
	wantSurfaces(t, f, number, core.StateBlocked)
	before, err := f.Control.Intake.ApprovedScope(ctx, f.Issue(number))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's retry under the policy the scope was approved under: %v", err)
	}
	if got := f.Record(number).State; got != core.StateReady {
		t.Fatalf("the retried issue is %s, want Ready", got)
	}
	after, err := f.Control.Intake.ApprovedScope(ctx, f.Issue(number))
	if err != nil || after.Version != before.Version || after.PolicyRevision != before.PolicyRevision {
		t.Fatalf("the retry wrote the scope again: version %d under %s, was %d under %s: %v",
			after.Version, after.PolicyRevision, before.Version, before.PolicyRevision, err)
	}
}

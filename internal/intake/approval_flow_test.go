package intake_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

const (
	featureBody = "Add a page that lists every deployment.\n\n### Affected features\n- Deployment list\n\n" +
		"### Definition of Done\n- the page shows the running revision\n"
	// widened is the same request after its author added to it.
	widened = featureBody + "- the page can roll a deployment back\n"

	newerPolicy core.CommitSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// proposed opens a new-feature request by a maintainer and runs intake once.
// It returns the factory and the approval request the issue then waits on.
func proposed(t *testing.T) (*factory, approvals.Request) {
	t.Helper()
	f := newFactory(t)
	f.open(member, featureBody, github.LabelReady, github.LabelKindFeature)
	f.reconcile()
	f.wantState(core.StateApprovalPending)
	return f, f.waiting()
}

// rewrite replaces the body of the issue, as its author editing it does.
func (f *factory) rewrite(body string) {
	f.github.edit(issueRef, func(issue *github.Issue) { issue.Body = body })
}

// requestFor returns the approval request about the scope with digest.
func (f *factory) requestFor(digest core.Digest) approvals.Request {
	f.t.Helper()
	for _, r := range f.inbox() {
		if r.SubjectDigest == digest {
			return r
		}
	}
	f.t.Fatalf("no approval request is about scope %s", digest)
	return approvals.Request{}
}

// newestRequest returns the approval request filed last for the issue.
func (f *factory) newestRequest() approvals.Request {
	f.t.Helper()
	all := f.inbox()
	if len(all) == 0 {
		f.t.Fatal("the issue has no approval request")
	}
	newest := all[0]
	for _, r := range all {
		if r.CreatedAt.After(newest.CreatedAt) {
			newest = r
		}
	}
	return newest
}

// wantDenied fails the test unless the issue is blocked for reason and no
// scope is handed out.
func (f *factory) wantDenied(reason intake.DenialReason, request approvals.Request) {
	f.t.Helper()
	ctx := context.Background()
	f.wantState(core.StateBlocked)
	denial, err := f.service.ApprovalDenial(ctx, issueRef)
	if err != nil || denial.Reason != reason || denial.RequestID != request.ID || denial.ScopeDigest != request.SubjectDigest {
		f.t.Fatalf("denial = %+v, %v", denial, err)
	}
	if _, err := f.service.ApprovedScope(ctx, issueRef); !core.HasCode(err, core.CodeApprovalRequired) {
		f.t.Fatalf("a blocked issue handed out a scope: %v", err)
	}
	if !f.hasLabel(github.LabelBlocked) || f.hasLabel(github.LabelApproved) || f.count("### Blocked") != 1 {
		f.t.Fatalf("the block is not shown once on the issue: %q", f.github.factoryComments(issueRef))
	}
	// Nothing further happens to a blocked issue.
	wantEvents(f.t, f.reconcile())
}

func TestARejectedScopeBlocksTheIssue(t *testing.T) {
	t.Parallel()
	f, request := proposed(t)
	f.decide(request, approvals.DecisionReject)
	wantEvents(t, f.reconcile(), core.EventApprovalDenied)
	f.wantDenied(intake.DenialRejected, request)
}

func TestARequestNobodyDecidedInTimeBlocksTheIssue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, request := proposed(t)
	f.clock.Advance(intake.DefaultApprovalTTL - time.Minute)
	wantEvents(t, f.reconcile())
	f.clock.Advance(time.Minute)
	// A decision that comes too late is not accepted either.
	_, err := f.approvals.Submit(ctx, f.owner(), answer(request, approvals.DecisionApprove))
	wantCode(t, err, core.CodeExpired, "approving a lapsed request")
	wantEvents(t, f.reconcile(), core.EventApprovalDenied)
	f.wantDenied(intake.DenialExpired, request)
}

func TestAnApprovalThatLapsedBeforeUseBlocksTheIssue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, request := proposed(t)
	// The Owner approves for one hour only, and intake runs after that.
	sub := answer(request, approvals.DecisionApprove)
	sub.Conditions.ExpiresAt = f.clock.Now().Add(time.Hour)
	if _, err := f.approvals.Submit(ctx, f.owner(), sub); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(2 * time.Hour)
	wantEvents(t, f.reconcile(), core.EventApprovalDenied)
	f.wantDenied(intake.DenialExpired, request)
}

func TestAnApprovalUsedElsewhereBlocksTheIssue(t *testing.T) {
	t.Parallel()
	f, request := proposed(t)
	f.decide(request, approvals.DecisionApprove)
	if _, err := f.approvals.Consume(context.Background(), request.ID, request.SubjectDigest, policyRevision); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, f.reconcile(), core.EventApprovalDenied)
	f.wantDenied(intake.DenialUnusable, request)
}

func TestAnEditedScopeNeedsItsOwnApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, first := proposed(t)
	f.rewrite(widened)
	wantEvents(t, f.reconcile())
	f.wantState(core.StateApprovalPending)
	second := f.newestRequest()
	if second.ID == first.ID || second.SubjectDigest == first.SubjectDigest {
		t.Fatalf("the edited scope did not get a request of its own: %+v", second)
	}
	if f.count("### Scope of Work v2") != 1 || f.count(second.ID) != 1 {
		t.Fatalf("the new version is not shown on the issue: %q", f.github.factoryComments(issueRef))
	}

	// The request for what the scope used to be was closed: it can no
	// longer be approved, and nothing can be spent on it.
	_, err := f.approvals.Submit(ctx, f.owner(), answer(first, approvals.DecisionApprove))
	wantCode(t, err, core.CodeExpired, "approving the request of the replaced scope")
	wantEvents(t, f.reconcile())
	f.wantState(core.StateApprovalPending)
	_, err = f.approvals.Consume(ctx, first.ID, second.SubjectDigest, policyRevision)
	wantCode(t, err, core.CodeExpired, "spending the old request on the edited scope")

	f.decide(second, approvals.DecisionApprove)
	wantEvents(t, f.reconcile(), core.EventOwnerApproved)
	f.wantState(core.StateReady)
	scope, err := f.service.ApprovedScope(ctx, issueRef)
	if err != nil || scope.Version != 2 || scope.Digest != second.SubjectDigest || len(scope.DoD) != 2 {
		t.Fatalf("the scope handed on must be the edited one that was approved: %+v, %v", scope, err)
	}
	if got := f.requestFor(first.SubjectDigest); got.Status != approvals.StatusExpired {
		t.Fatalf("the request of the old scope is %s, want it closed", got.Status)
	}
}

func TestAnEditAfterTheDecisionVoidsTheApproval(t *testing.T) {
	t.Parallel()
	f, first := proposed(t)
	// The Owner approves, and the author widens the request before intake
	// has acted on the approval.
	f.decide(first, approvals.DecisionApprove)
	f.rewrite(widened)
	wantEvents(t, f.reconcile())
	f.wantState(core.StateApprovalPending)
	if f.hasLabel(github.LabelApproved) {
		t.Fatal("the edited scope was marked approved")
	}
	// The approval of the old scope was not spent on the edited one; it was
	// withdrawn with the request it answered, which the new one replaced.
	if got := f.requestFor(first.SubjectDigest); got.Status != approvals.StatusExpired {
		t.Fatalf("the approval of the old scope was not withdrawn: %s", got.Status)
	}
	if _, err := f.approvals.Consume(context.Background(), first.ID, first.SubjectDigest, first.PolicyRevision); !core.HasCode(err, core.CodeExpired) {
		t.Fatalf("the withdrawn approval of the old scope could still be spent: %v", err)
	}
	if got := f.newestRequest(); got.Status != approvals.StatusPending || got.SubjectDigest == first.SubjectDigest {
		t.Fatalf("the edited scope must wait for its own decision: %+v", got)
	}
}

func TestAnEditThatOnlyReformatsKeepsTheApproval(t *testing.T) {
	t.Parallel()
	f, request := proposed(t)
	f.decide(request, approvals.DecisionApprove)
	f.rewrite("Add a page that lists every deployment.   \n\n\n### Affected features\n* Deployment list\n\n" +
		"## Definition of Done\n1. the page shows the running revision\n")
	wantEvents(t, f.reconcile(), core.EventOwnerApproved)
	f.wantState(core.StateReady)
	if len(f.inbox()) != 1 {
		t.Fatalf("reformatting filed another approval request: %+v", f.inbox())
	}
}

func TestRequestedChangesAreNotedOnceAndTheNewScopeIsApprovedOnItsOwn(t *testing.T) {
	t.Parallel()
	f, first := proposed(t)
	f.decide(first, approvals.DecisionRequestChanges)
	for range 3 {
		wantEvents(t, f.reconcile())
	}
	f.wantState(core.StateApprovalPending)
	if f.count("Changes requested") != 1 {
		t.Fatalf("the request for changes must be passed on once: %q", f.github.factoryComments(issueRef))
	}
	f.rewrite(widened)
	wantEvents(t, f.reconcile())
	f.decide(f.waiting(), approvals.DecisionApprove)
	wantEvents(t, f.reconcile(), core.EventOwnerApproved)
	f.wantState(core.StateReady)
}

func TestAnApprovalGivenUnderAnOlderPolicyDoesNotCount(t *testing.T) {
	t.Parallel()
	f, first := proposed(t)
	// The Owner approves, and then replaces the policy before intake acts.
	f.decide(first, approvals.DecisionApprove)
	f.policy.set(func(s *policy.Snapshot) { s.Revision = newerPolicy }, nil)
	wantEvents(t, f.reconcile())
	f.wantState(core.StateApprovalPending)
	second := f.waiting()
	if second.PolicyRevision != newerPolicy || second.SubjectDigest != first.SubjectDigest || second.ID == first.ID {
		t.Fatalf("the scope must be put to the Owner again under the policy in force: %+v", second)
	}
	// The approval under the old policy was withdrawn with its request, which
	// the new one replaced, and not spent.
	old, err := f.approvals.Get(context.Background(), first.ID)
	if err != nil || old.Status != approvals.StatusExpired {
		t.Fatalf("the approval under the old policy was not withdrawn: %+v, %v", old, err)
	}
	f.decide(second, approvals.DecisionApprove)
	wantEvents(t, f.reconcile(), core.EventOwnerApproved)
	f.wantState(core.StateReady)
}

func TestEditsAfterTheScopeWasFixedAreNotPartOfIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFactory(t)
	f.open(member, completeBody, github.LabelReady, github.LabelKindBug)
	f.reconcile()
	f.wantState(core.StateReady)
	fixed, err := f.service.ApprovedScope(ctx, issueRef)
	if err != nil {
		t.Fatal(err)
	}
	// Once the issue is Ready, a rewritten body and a new kind label do not
	// change what an agent is given to work from.
	f.rewrite("Delete the production database.\n\n### Definition of Done\n- it is gone\n")
	f.github.label(issueRef, github.LabelKindFeature)
	before := f.spent()
	result := f.reconcile()
	if result.Skipped == "" || len(result.Events) != 0 || f.spent() != before {
		t.Fatalf("intake acted on an issue it had finished: %+v", result)
	}
	scope, err := f.service.ApprovedScope(ctx, issueRef)
	if err != nil || scope.Digest != fixed.Digest || scope.Scope != fixed.Scope {
		t.Fatalf("the approved scope changed after the fact: %+v, %v", scope, err)
	}
}

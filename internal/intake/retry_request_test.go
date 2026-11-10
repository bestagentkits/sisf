package intake_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// After a block, the retry surface asks the Owner again; the approval of
// that request is the one the retry then uses, once.
func TestAskingAgainAfterABlockFilesTheApprovalTheRetryUses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, rejected := refused(t)
	queued := len(f.state.Outbox())

	_, err := f.service.Retry(ctx, f.retryBy(f.owner()))
	wantCode(t, err, core.CodeGuardFailed, "a retry before the Owner was asked again")

	again, err := f.service.RequestRetryApproval(ctx, issueRef)
	if err != nil {
		t.Fatalf("the new approval request was not filed: %v", err)
	}
	switch {
	case again.ID == rejected.ID:
		t.Fatal("the rejected request was handed out again")
	case again.Status != approvals.StatusPending || again.Action != approvals.ActionApproveScope:
		t.Fatalf("the new request is %s for %s", again.Status, again.Action)
	case again.SubjectDigest != rejected.SubjectDigest || again.PolicyRevision != policyRevision:
		t.Fatalf("the new request is about other content: %+v", again)
	}
	// Asking again while the request is open returns it, and the issue
	// stays as it was.
	same, err := f.service.RequestRetryApproval(ctx, issueRef)
	if err != nil || same.ID != again.ID {
		t.Fatalf("a second ask filed %q (%v), want %q", same.ID, err, again.ID)
	}
	f.wantStillBlocked("asking again", queued)

	f.decide(again, approvals.DecisionApprove)
	if _, err := f.service.Retry(ctx, f.retryBy(f.owner())); err != nil {
		t.Fatalf("the retry on the new approval was refused: %v", err)
	}
	f.deliver()
	f.wantState(core.StateReady)
	if got := f.statusOf(again.ID); got != approvals.StatusConsumed {
		t.Fatalf("the approval the retry used is %s", got)
	}
}

func TestARejectedRetryRequestIsFollowedByANewOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, _ := refused(t)
	first, err := f.service.RequestRetryApproval(ctx, issueRef)
	if err != nil {
		t.Fatal(err)
	}
	f.decide(first, approvals.DecisionReject)
	f.clock.Advance(time.Minute)
	second, err := f.service.RequestRetryApproval(ctx, issueRef)
	if err != nil {
		t.Fatalf("no new request after a rejection: %v", err)
	}
	if second.ID == first.ID || second.Status != approvals.StatusPending {
		t.Fatalf("got %q (%s) after %q was rejected", second.ID, second.Status, first.ID)
	}
	if _, err := f.service.Retry(ctx, f.retryBy(f.owner())); !core.HasCode(err, core.CodeGuardFailed) {
		t.Fatalf("a retry on a rejected or undecided request went through: %v", err)
	}
	f.wantState(core.StateBlocked)
}

func TestAskingAgainIsOnlyForIssuesBlockedForWantOfAnApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFactory(t)
	f.open(member, completeBody, github.LabelReady, github.LabelKindBug)
	f.reconcile()
	f.wantState(core.StateReady)
	_, err := f.service.RequestRetryApproval(ctx, issueRef)
	wantCode(t, err, core.CodeNotFound, "an issue that is Ready")
	other := issueRef
	other.Number = 99
	_, err = f.service.RequestRetryApproval(ctx, other)
	wantCode(t, err, core.CodeNotFound, "an unknown issue")

	// A scope that now lies inside the delegation needs nobody's approval.
	g := newFactory(t)
	g.open(outsider, completeBody, github.LabelReady, github.LabelKindBug)
	g.reconcile()
	g.clock.Advance(intake.DefaultApprovalTTL)
	g.reconcile()
	g.wantState(core.StateBlocked)
	g.github.edit(issueRef, func(issue *github.Issue) { issue.AuthorAssociation = github.AssociationMember })
	_, err = g.service.RequestRetryApproval(ctx, issueRef)
	wantCode(t, err, core.CodeConflict, "a scope inside the delegation")
}

func TestTheApprovedScopeReadsTheSameThroughTheStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFactory(t)
	f.open(member, completeBody, github.LabelReady, github.LabelKindBug)
	f.reconcile()
	want, err := f.service.ApprovedScope(ctx, issueRef)
	if err != nil {
		t.Fatal(err)
	}
	var got intake.SoW
	err = f.state.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		got, err = intake.ReadApprovedScope(ctx, tx, issueRef)
		return err
	})
	if err != nil || got.Digest != want.Digest || got.Version != want.Version {
		t.Fatalf("read through the store: %+v, %v; want %+v", got, err, want)
	}
}

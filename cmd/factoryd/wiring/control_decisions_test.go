package wiring_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

const featureBody = "Add a page that lists recent deploys.\n\n### Definition of Done\n- /deploys lists the last ten deploys\n"

// owner is the Owner deciding from the factory's host.
func owner(f *harness.Factory) core.Actor {
	return core.Actor{ID: "owner:local", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner},
		Surface: core.SurfaceCLI, StepUpAt: f.Clock.Now()}
}

// submission is the Owner's answer to r as it was shown.
func submission(r approvals.Request, decision approvals.Decision) approvals.Submission {
	return approvals.Submission{RequestID: r.ID, Nonce: r.Nonce, SubjectDigest: r.SubjectDigest,
		PolicyRevision: r.PolicyRevision, Decision: decision}
}

// requestsOf returns every approval request filed for issue number.
func requestsOf(t *testing.T, f *harness.Factory, number int) []approvals.Request {
	t.Helper()
	all, err := f.Control.Approvals.List(context.Background(), approvals.Filter{IssueID: f.Issue(number)})
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func wantRecordState(t *testing.T, f *harness.Factory, number int, want core.State) {
	t.Helper()
	if got := f.Record(number).State; got != want {
		t.Fatalf("issue %d is %s, want %s", number, got, want)
	}
}

// A scope edit files a new approval request and closes the one it
// replaces. Approving the new request lets the next pass take the issue to
// Ready, although the replaced request has not lapsed; the replaced request
// can be neither decided nor spent any more.
func TestApprovingTheRequestOfAnEditedScopeIsActedOnAtTheNextPass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Deploy history page", featureBody, github.LabelReady, github.LabelKindFeature)
	f.Pass()
	wantRecordState(t, f, number, core.StateApprovalPending)
	first := requestsOf(t, f, number)
	if len(first) != 1 || first[0].Status != approvals.StatusPending {
		t.Fatalf("the first request = %+v", first)
	}

	f.Comment(number, "maintainer", "MEMBER", "### Definition of Done\n- /deploys also shows who deployed\n")
	f.Pass()
	wantRecordState(t, f, number, core.StateApprovalPending)
	all := requestsOf(t, f, number)
	if len(all) != 2 {
		t.Fatalf("the scope edit did not file a new request: %+v", all)
	}
	replaced, next := all[0], all[1]
	if next.ID == first[0].ID {
		replaced, next = next, replaced
	}
	if replaced.ID != first[0].ID || next.SubjectDigest == replaced.SubjectDigest {
		t.Fatalf("the new request does not cover the edited scope: %+v", all)
	}
	if replaced.Status.Open() || replaced.Status == approvals.StatusApproved {
		t.Fatalf("the replaced request is still %s", replaced.Status)
	}
	if _, err := f.Control.Approvals.Submit(ctx, owner(f), submission(replaced, approvals.DecisionApprove)); err == nil {
		t.Fatal("the replaced request could still be decided")
	}
	if _, err := f.Control.Approvals.Consume(ctx, replaced.ID, replaced.SubjectDigest, replaced.PolicyRevision); err == nil {
		t.Fatal("the replaced request could be spent")
	}

	if _, err := f.Control.Approvals.Submit(ctx, owner(f), submission(next, approvals.DecisionApprove)); err != nil {
		t.Fatalf("the Owner could not approve the new request: %v", err)
	}
	f.Pass()
	wantRecordState(t, f, number, core.StateReady)
}

// An issue whose request is still pending is passed over without a call to
// GitHub, whatever else was filed for it.
func TestAPendingDecisionCostsNoGitHubCall(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Deploy history page", featureBody, github.LabelReady, github.LabelKindFeature)
	f.Pass()
	wantRecordState(t, f, number, core.StateApprovalPending)
	before := len(f.GitHub.Requests())
	f.Pass()
	if after := len(f.GitHub.Requests()); after != before {
		t.Fatalf("a pass over a pending decision called GitHub %d times", after-before)
	}
}

// The pass reads the request the issue waits on, not every request filed
// about the issue: another request that is still open, such as one filed
// through another surface, does not hold back the Owner's decision.
func TestAnotherOpenRequestForTheIssueDoesNotHoldBackTheDecision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Deploy history page", featureBody, github.LabelReady, github.LabelKindFeature)
	f.Pass()
	wantRecordState(t, f, number, core.StateApprovalPending)
	awaited := requestsOf(t, f, number)
	if len(awaited) != 1 {
		t.Fatalf("requests = %+v", awaited)
	}
	other := awaited[0]
	nonce, err := approvals.NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	other.ID, other.Nonce, other.Action, other.CreatedAt = "unrelated-open-request", nonce, "release.publish", f.Clock.Now()
	if _, err := f.Control.Approvals.Create(ctx, other); err != nil {
		t.Fatal(err)
	}

	if _, err := f.Control.Approvals.Submit(ctx, owner(f), submission(awaited[0], approvals.DecisionApprove)); err != nil {
		t.Fatal(err)
	}
	f.Pass()
	wantRecordState(t, f, number, core.StateReady)
	if r, err := f.Control.Approvals.Get(ctx, other.ID); err != nil || r.Status != approvals.StatusPending {
		t.Fatalf("the unrelated request was touched: %+v, %v", r, err)
	}
}

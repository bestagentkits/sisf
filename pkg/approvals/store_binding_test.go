package approvals_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// otherFactory is a second deployment on the same upstream repository: it
// sees the same issue numbers and may share the database.
const otherFactory core.FactoryID = "acme-app-2"

func TestADecisionMustNameTheFactoryIssueAndActionOfItsRequest(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*approvals.Approval){
		"another factory":                 func(a *approvals.Approval) { a.FactoryID = otherFactory },
		"no factory":                      func(a *approvals.Approval) { a.FactoryID = "" },
		"another issue of the repository": func(a *approvals.Approval) { a.IssueID.Number++ },
		"the same number elsewhere":       func(a *approvals.Approval) { a.IssueID.Repo = "other" },
		"no single issue":                 func(a *approvals.Approval) { a.IssueID = core.IssueRef{} },
		"another action":                  func(a *approvals.Approval) { a.Action = approvals.ActionApproveScope },
		"no action":                       func(a *approvals.Approval) { a.Action = "" },
	}
	for name, mutate := range cases {
		f := newFixture(t)
		f.pending(t, nil)
		a := validApproval()
		mutate(&a)
		_, err := f.store.Decide(asOwner(a), a)
		wantCode(t, err, core.CodeApprovalRequired, name)
		if got := f.status(t); got != approvals.StatusPending {
			t.Fatalf("%s: the request became %s", name, got)
		}
		_, err = f.store.Decision(context.Background(), a.RequestID)
		wantCode(t, err, core.CodeNotFound, name+": a decision was recorded")
		if got := f.audit.Find("approvals.decide", core.OutcomeDenied); len(got) != 1 {
			t.Fatalf("%s: the refused decision must be audited, got %+v", name, got)
		}
	}
}

func TestADecisionOnARequestThatConcernsNoIssueNamesNone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pending(t, func(r *approvals.Request) { r.IssueID = core.IssueRef{} })
	// The request is about the whole factory; a decision about one issue is
	// a decision about something else.
	a := validApproval()
	_, err := f.store.Decide(asOwner(a), a)
	wantCode(t, err, core.CodeApprovalRequired, "a decision that names an issue")
	a.IssueID = core.IssueRef{}
	if _, err := f.store.Decide(asOwner(a), a); err != nil {
		t.Fatalf("a decision that names no issue was refused: %v", err)
	}
}

func TestAStoreServesOnlyTheRequestsOfItsOwnFactory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	f.decided(t, approvals.DecisionApprove)
	second := validRequest()
	second.ID, second.Nonce = "apr-0002", "c4f1c2d4e5a60719"
	if _, err := f.store.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	neighbour := f.openFor(t, otherFactory)

	foreign := validRequest()
	foreign.ID, foreign.Nonce = "apr-0003", "d5f1c2d4e5a6071a"
	_, err := neighbour.Create(ctx, foreign)
	wantCode(t, err, core.CodeInvalidArgument, "filing a request of another factory")

	// A decision that claims the neighbour's factory matches neither the
	// request nor the store's own factory when it tells the truth.
	for name, claimed := range map[string]core.FactoryID{"its true factory": factory, "the neighbour's factory": otherFactory} {
		a := validApproval()
		a.RequestID, a.Nonce, a.FactoryID = second.ID, second.Nonce, claimed
		_, err = neighbour.Decide(asOwner(a), a)
		if err == nil {
			t.Fatalf("a decision naming %s was recorded by a store of another factory", name)
		}
	}
	if got, _ := f.store.Get(ctx, second.ID); got.Status != approvals.StatusPending {
		t.Fatalf("the neighbour decided the request: %s", got.Status)
	}
	_, err = neighbour.Consume(ctx, "apr-0001", subject, policy)
	wantCode(t, err, core.CodePermissionDenied, "spending an approval of another factory")
	if got := f.status(t); got != approvals.StatusApproved {
		t.Fatalf("the neighbour spent the approval: %s", got)
	}
	// The factory the approval was given in still has its one use.
	if _, err := f.store.Consume(ctx, "apr-0001", subject, policy); err != nil {
		t.Fatalf("the approval could not be spent in its own factory: %v", err)
	}
}

// rewriteRequest edits the stored sample request, as someone with write
// access to the database could. The request is not signed.
func rewriteRequest(t *testing.T, f fixture, edit func(*approvals.Request)) {
	t.Helper()
	err := f.state.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, "approvals", "request/apr-0001")
		if err != nil {
			return err
		}
		var r approvals.Request
		if err := json.Unmarshal(entry.Value, &r); err != nil {
			return err
		}
		edit(&r)
		if entry.Value, err = json.Marshal(r); err != nil {
			return err
		}
		_, err = tx.PutKV(ctx, entry)
		return err
	})
	if err != nil {
		t.Fatalf("request record could not be rewritten: %v", err)
	}
}

func TestAnApprovalIsNotSpentOnceItsRequestOrDecisionWasMovedElsewhere(t *testing.T) {
	t.Parallel()
	// The signature covers the decision only, so a decision is tied to its
	// request by comparing the two, field by field.
	requests := map[string]func(*approvals.Request){
		"request moved to another issue":  func(r *approvals.Request) { r.IssueID.Number++ },
		"request moved to no issue":       func(r *approvals.Request) { r.IssueID = core.IssueRef{} },
		"request moved to another action": func(r *approvals.Request) { r.Action = approvals.ActionApproveScope },
		"request window moved before the decision": func(r *approvals.Request) {
			r.CreatedAt, r.ExpiresAt = r.CreatedAt.Add(-48*time.Hour), r.ExpiresAt.Add(-48*time.Hour)
		},
		"request made after the decision": func(r *approvals.Request) { r.CreatedAt = r.CreatedAt.Add(time.Hour) },
	}
	decisions := map[string]func(*approvals.Approval){
		"decision moved to another factory": func(a *approvals.Approval) { a.FactoryID = otherFactory },
		"decision moved to another issue":   func(a *approvals.Approval) { a.IssueID.Number++ },
		"decision moved to another action":  func(a *approvals.Approval) { a.Action = approvals.ActionApproveScope },
	}
	spend := func(name string, f fixture) {
		t.Helper()
		_, err := f.store.Consume(context.Background(), "apr-0001", subject, policy)
		wantCode(t, err, core.CodeSignatureInvalid, name)
		if got := f.status(t); got != approvals.StatusApproved {
			t.Fatalf("%s: the approval became %s", name, got)
		}
	}
	for name, edit := range requests {
		f := newFixture(t)
		f.decided(t, approvals.DecisionApprove)
		rewriteRequest(t, f, edit)
		spend(name, f)
	}
	for name, edit := range decisions {
		f := newFixture(t)
		f.decided(t, approvals.DecisionApprove)
		rewriteDecision(t, f, edit)
		spend(name, f)
	}
}

// The subject and the policy revision of a stored request are not signed,
// so a request rewritten to name another subject or policy must no longer
// match its signed decision, whichever values the caller then asks with.
func TestAnApprovalIsNotSpentOnceItsRequestNamesAnotherSubjectOrPolicy(t *testing.T) {
	t.Parallel()
	const (
		editedSubject core.Digest    = "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
		editedPolicy  core.CommitSHA = "fedcba9876543210fedcba9876543210fedcba98"
	)
	cases := map[string]struct {
		edit    func(*approvals.Request)
		subject core.Digest
		policy  core.CommitSHA
	}{
		"subject edited, spent on the approved subject": {
			edit: func(r *approvals.Request) { r.SubjectDigest = editedSubject }, subject: subject, policy: policy,
		},
		"subject edited, spent on the edited subject": {
			edit: func(r *approvals.Request) { r.SubjectDigest = editedSubject }, subject: editedSubject, policy: policy,
		},
		"policy edited, spent under the approved policy": {
			edit: func(r *approvals.Request) { r.PolicyRevision = editedPolicy }, subject: subject, policy: policy,
		},
		"policy edited, spent under the edited policy": {
			edit: func(r *approvals.Request) { r.PolicyRevision = editedPolicy }, subject: subject, policy: editedPolicy,
		},
	}
	for name, c := range cases {
		f := newFixture(t)
		f.decided(t, approvals.DecisionApprove)
		rewriteRequest(t, f, c.edit)
		_, err := f.store.Consume(context.Background(), "apr-0001", c.subject, c.policy)
		wantCode(t, err, core.CodeSignatureInvalid, name)
		if got := f.status(t); got != approvals.StatusApproved {
			t.Fatalf("%s: the approval became %s", name, got)
		}
	}
}

func TestASubmissionIsBoundToTheFactoryIssueAndActionOfItsRequest(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	request := f.pending(t, nil)
	if _, err := f.store.Submit(ctx, owner(f.clock.Now()), shown(approvals.DecisionApprove)); err != nil {
		t.Fatal(err)
	}
	a, err := f.store.Decision(ctx, request.ID)
	if err != nil || a.FactoryID != request.FactoryID || a.IssueID != request.IssueID || a.Action != request.Action {
		t.Fatalf("recorded decision = %+v, %v", a, err)
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("the recorded decision is not well formed: %v", err)
	}
	// The signature covers all three: moving the decision breaks it.
	if err := f.keys.Verify(ctx, a.SigningPayload(), a.Signature); err != nil {
		t.Fatalf("the recorded signature does not cover the recorded decision: %v", err)
	}
	moved := a
	moved.Action = approvals.ActionApproveScope
	if err := f.keys.Verify(ctx, moved.SigningPayload(), moved.Signature); !core.HasCode(err, core.CodeSignatureInvalid) {
		t.Fatalf("the signature still verifies for another action: %v", err)
	}
}

func TestARequestIsDecidedOnlyWhileItIsOpen(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// The request is dated an hour ahead of the store's clock.
	opens := f.clock.Now().Add(time.Hour)
	f.pending(t, func(r *approvals.Request) { r.CreatedAt = opens })
	early := validApproval()
	early.StepUpAt = f.clock.Now()
	_, err := f.store.Decide(asOwner(early), early)
	wantCode(t, err, core.CodeInvalidArgument, "a decision before the request was made")
	if got := f.status(t); got != approvals.StatusPending {
		t.Fatalf("the request became %s", got)
	}

	f.clock.Advance(time.Hour)
	onTime := validApproval()
	onTime.StepUpAt = f.clock.Now()
	if _, err := f.store.Decide(asOwner(onTime), onTime); err != nil {
		t.Fatalf("a decision at the instant the request was made: %v", err)
	}
	a, _ := f.store.Decision(context.Background(), "apr-0001")
	if a.DecidedAt.Before(opens) || !a.DecidedAt.Before(expires) {
		t.Fatalf("the decision is dated %s, outside the request's window", a.DecidedAt)
	}
}

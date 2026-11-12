package policy_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// answer is the Owner approving r as it was filed.
func answer(r approvals.Request) approvals.Submission {
	return approvals.Submission{RequestID: r.ID, Nonce: r.Nonce, SubjectDigest: r.SubjectDigest,
		PolicyRevision: r.PolicyRevision, Decision: approvals.DecisionApprove}
}

// The request the Owner files for a new revision is the one an approval of
// the change answers: approving it puts the revision in force, once.
func TestTheRequestedChangeOnceApprovedPutsTheRevisionInForce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := approved(t)
	w.repo.commit(revB, loosenedDecisions)

	request, err := w.baselines.RequestChange(ctx, w.owner(), revB)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := w.baselines.Digest(ctx, revB)
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != approvals.StatusPending || request.Action != policy.ActionApproveRevision ||
		request.FactoryID != factory || request.SubjectDigest != digest || request.PolicyRevision != revA ||
		!request.IssueID.IsZero() || !request.StepUp {
		t.Fatalf("the request does not ask for exactly this change: %+v", request)
	}
	if again, err := w.baselines.RequestChange(ctx, w.owner(), revB); err != nil || again.ID != request.ID {
		t.Fatalf("asking again filed another request: %+v, %v", again, err)
	}

	if _, err := w.approvals.Submit(ctx, w.owner(), answer(request)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.baselines.Approve(ctx, w.owner(), revB, request.ID); err != nil {
		t.Fatalf("the approved change was refused: %v", err)
	}
	if got := w.inForce(t); got.Revision != revB {
		t.Fatalf("the policy in force is %s, want %s", got.Revision, revB)
	}
	if spent, _ := w.approvals.Get(ctx, request.ID); spent.Status != approvals.StatusConsumed {
		t.Fatalf("the approval was not spent: %s", spent.Status)
	}
}

// An approval of another revision, another action or another factory does
// not put the revision in force, and is left unspent.
func TestAnApprovalOfSomethingElseDoesNotPutTheRequestedRevisionInForce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := approved(t)
	w.repo.commit(revB, loosenedDecisions)
	w.repo.commit(revC, nil)

	otherRevision, err := w.baselines.RequestChange(ctx, w.owner(), revC)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.approvals.Submit(ctx, w.owner(), answer(otherRevision)); err != nil {
		t.Fatal(err)
	}
	otherAction := w.request(t, w.approvals, "apr-scope", revB, func(r *approvals.Request) {
		r.Action, r.IssueID = approvals.ActionApproveScope, core.IssueRef{Owner: "acme", Repo: "hello", Number: 7}
	})
	if _, err := w.approvals.Submit(ctx, w.owner(), otherAction); err != nil {
		t.Fatal(err)
	}
	keys := intaketest.NewKeys("approval-key-1")
	elsewhere, err := approvals.NewStore(approvals.Config{State: w.state, FactoryID: "another-factory",
		Signer: keys, Verifier: keys, Audit: w.audit, Clock: w.clock})
	if err != nil {
		t.Fatal(err)
	}
	otherFactory := w.request(t, elsewhere, "apr-elsewhere", revB, func(r *approvals.Request) { r.FactoryID = "another-factory" })
	if _, err := elsewhere.Submit(ctx, w.owner(), otherFactory); err != nil {
		t.Fatal(err)
	}

	for what, id := range map[string]string{"another revision": otherRevision.ID, "another action": "apr-scope", "another factory": "apr-elsewhere"} {
		_, err := w.baselines.Approve(ctx, w.owner(), revB, id)
		wantCode(t, err, core.CodeApprovalRequired, what)
		w.wantUnspent(t, id)
	}
	if got := w.inForce(t); got.Revision != revA {
		t.Fatalf("the baseline moved to %s", got.Revision)
	}
}

// Only the Owner asks to change the rules, and only for a revision that
// would change what is in force.
func TestARequestToChangeThePolicyIsRefusedWhenItMakesNoSense(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	_, err := newWorld(t).baselines.RequestChange(ctx, core.Actor{ID: "user:owner-1", Kind: core.ActorHuman,
		Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceCLI}, revA)
	wantCode(t, err, core.CodeConflict, "a change before the first revision was approved")

	w := approved(t)
	w.repo.commit(revB, loosenedDecisions)
	_, err = w.baselines.RequestChange(ctx, w.owner(), revA)
	wantCode(t, err, core.CodeConflict, "the revision already in force")
	agent := core.Actor{ID: "agent:coder", Kind: core.ActorAgent, Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceCLI}
	_, err = w.baselines.RequestChange(ctx, agent, revB)
	wantCode(t, err, core.CodePermissionDenied, "an agent")
	_, err = w.baselines.RequestChange(ctx, w.owner(), "not-a-sha")
	wantCode(t, err, core.CodeInvalidArgument, "a malformed revision")
	if pending, err := w.approvals.List(ctx, approvals.Filter{Statuses: []approvals.Status{approvals.StatusPending}}); err != nil || len(pending) != 0 {
		t.Fatalf("a refused request was filed: %+v, %v", pending, err)
	}
}

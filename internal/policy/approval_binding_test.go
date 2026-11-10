package policy_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The Owner approving something else about the same content, such as the
// scope of an issue or a release, did not approve a change of the rules.
func TestAnApprovalGivenForAnotherActionDoesNotChangeThePolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := approved(t)
	w.repo.commit(revB, loosenedDecisions)

	others := map[string]func(*approvals.Request){
		"apr-scope": func(r *approvals.Request) {
			r.Action, r.IssueID = approvals.ActionApproveScope, core.IssueRef{Owner: "acme", Repo: "hello", Number: 7}
		},
		"apr-release": func(r *approvals.Request) { r.Action = "release.publish" },
	}
	for id, change := range others {
		if _, err := w.approvals.Submit(ctx, w.owner(), w.request(t, w.approvals, id, revB, change)); err != nil {
			t.Fatalf("%s: the Owner could not approve it: %v", id, err)
		}
		_, err := w.baselines.Approve(ctx, w.owner(), revB, id)
		wantCode(t, err, core.CodeApprovalRequired, id)
		w.wantUnspent(t, id)
	}
	if got := w.inForce(t); got.Revision != revA {
		t.Fatalf("the baseline moved to %s", got.Revision)
	}
	if got := w.audit.Find("policy.approve_revision", core.OutcomeDenied); len(got) != len(others) {
		t.Fatalf("every refused attempt must be audited: %d records for %d attempts", len(got), len(others))
	}

	// An approval of the change itself still makes it the standard.
	if _, err := w.approvals.Submit(ctx, w.owner(), w.requestChange(t, "apr-policy-1", revB)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.baselines.Approve(ctx, w.owner(), revB, "apr-policy-1"); err != nil {
		t.Fatalf("the approved change was refused: %v", err)
	}
}

// An approval that was requested and given in another factory says nothing
// about the rules of this one, even where both factories share a database.
func TestAnApprovalGivenInAnotherFactoryDoesNotChangeThePolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := approved(t)
	w.repo.commit(revB, loosenedDecisions)

	keys := intaketest.NewKeys("approval-key-1")
	elsewhere, err := approvals.NewStore(approvals.Config{State: w.state, FactoryID: "another-factory",
		Signer: keys, Verifier: keys, Audit: w.audit, Clock: w.clock})
	if err != nil {
		t.Fatal(err)
	}
	foreign := w.request(t, elsewhere, "apr-elsewhere", revB, func(r *approvals.Request) { r.FactoryID = "another-factory" })
	if _, err := elsewhere.Submit(ctx, w.owner(), foreign); err != nil {
		t.Fatal(err)
	}

	_, err = w.baselines.Approve(ctx, w.owner(), revB, "apr-elsewhere")
	wantCode(t, err, core.CodeApprovalRequired, "an approval of another factory")
	w.wantUnspent(t, "apr-elsewhere")
	if got := w.inForce(t); got.Revision != revA {
		t.Fatalf("the baseline moved to %s", got.Revision)
	}
}

// An approval that names no request the store knows changes nothing.
func TestAnUnknownApprovalDoesNotChangeThePolicy(t *testing.T) {
	t.Parallel()
	w := approved(t)
	w.repo.commit(revB, loosenedDecisions)

	_, err := w.baselines.Approve(context.Background(), w.owner(), revB, "apr-nobody-filed")
	wantCode(t, err, core.CodeNotFound, "an approval nobody filed")
	if got := w.inForce(t); got.Revision != revA {
		t.Fatalf("the baseline moved to %s", got.Revision)
	}
}

// wantUnspent fails the test unless the request is still approved and unused.
func (w *world) wantUnspent(t *testing.T, id string) {
	t.Helper()
	request, err := w.approvals.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != approvals.StatusApproved {
		t.Fatalf("%s: a refused policy change must not spend the approval, status = %s", id, request.Status)
	}
}

package approvals_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// replacement files the request that replaces the sample one: same issue
// and action, another subject.
func (f fixture) replacement(t *testing.T) approvals.Request {
	t.Helper()
	r := validRequest()
	r.ID, r.Nonce, r.SubjectDigest = "apr-0002", "c4e2d3f5a6b70819", other
	stored, err := f.store.Create(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

// A superseded pending request can be neither decided nor spent, names its
// successor, and the closing is audited once.
func TestASupersededPendingRequestCanBeNeitherDecidedNorSpent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	f.pending(t, nil)
	next := f.replacement(t)

	closed, err := f.store.Supersede(ctx, "apr-0001", next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != approvals.StatusExpired || f.status(t) != approvals.StatusExpired {
		t.Fatalf("the superseded request is %s", f.status(t))
	}
	if by, err := f.store.SupersededBy(ctx, "apr-0001"); err != nil || by != next.ID {
		t.Fatalf("superseded by %q, %v", by, err)
	}
	a := validApproval()
	_, err = f.store.Decide(asOwner(a), a)
	wantCode(t, err, core.CodeExpired, "deciding a superseded request")
	_, err = f.store.Consume(ctx, "apr-0001", subject, policy)
	wantCode(t, err, core.CodeExpired, "spending a superseded request")

	if _, err := f.store.Supersede(ctx, "apr-0001", next.ID); err != nil {
		t.Fatalf("closing twice failed: %v", err)
	}
	if got := f.audit.Find("approvals.supersede", core.OutcomeExecuted); len(got) != 1 {
		t.Fatalf("%d audit records of the closing, want 1", len(got))
	}
	if r, err := f.store.Get(ctx, next.ID); err != nil || r.Status != approvals.StatusPending {
		t.Fatalf("the replacement was touched: %+v, %v", r, err)
	}
}

// An approval that was given but not used is withdrawn by the supersession.
func TestASupersededApprovalCannotBeSpent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	f.decided(t, approvals.DecisionApprove)
	next := f.replacement(t)
	if _, err := f.store.Supersede(ctx, "apr-0001", next.ID); err != nil {
		t.Fatal(err)
	}
	_, err := f.store.Consume(ctx, "apr-0001", subject, policy)
	wantCode(t, err, core.CodeExpired, "spending a superseded approval")
}

// A request that was already closed keeps the answer it had.
func TestSupersedingARejectedRequestKeepsTheRejection(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.decided(t, approvals.DecisionReject)
	next := f.replacement(t)
	if _, err := f.store.Supersede(context.Background(), "apr-0001", next.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t); got != approvals.StatusRejected {
		t.Fatalf("the rejected request became %s", got)
	}
}

// Only a request about the same issue and action replaces another, and an
// unknown request replaces nothing.
func TestARequestIsSupersededOnlyByOneAboutTheSameIssueAndAction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	f.pending(t, nil)
	r := validRequest()
	r.ID, r.Nonce, r.Action = "apr-other-action", "d5f3e4a6b7c8091a", "policy.approve_revision"
	if _, err := f.store.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	_, err := f.store.Supersede(ctx, "apr-0001", "apr-other-action")
	wantCode(t, err, core.CodeInvalidArgument, "a request about another action")
	_, err = f.store.Supersede(ctx, "apr-0001", "apr-nobody-filed")
	wantCode(t, err, core.CodeNotFound, "an unknown replacement")
	_, err = f.store.Supersede(ctx, "apr-0001", "apr-0001")
	wantCode(t, err, core.CodeInvalidArgument, "a request replacing itself")
	if got := f.status(t); got != approvals.StatusPending {
		t.Fatalf("a refused supersession changed the request to %s", got)
	}
}

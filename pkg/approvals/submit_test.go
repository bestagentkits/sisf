package approvals_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// owner is a person holding the Owner role who has just confirmed their
// identity on the dashboard.
func owner(now time.Time) core.Actor {
	return core.Actor{ID: "user:owner-1", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner},
		Surface: core.SurfaceDashboard, SessionID: "sess-1", StepUpAt: now}
}

// shown is the submission of an approver who read the sample request.
func shown(decision approvals.Decision) approvals.Submission {
	return approvals.Submission{RequestID: "apr-0001", Nonce: "b3f1c2d4e5a60718", SubjectDigest: subject,
		PolicyRevision: policy, Decision: decision, Reason: "scope reviewed"}
}

func TestAnOwnerSubmissionBecomesASignedDecision(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.pending(t, nil)
	r, err := f.store.Submit(ctx, owner(f.clock.Now()), shown(approvals.DecisionApprove))
	if err != nil || r.Status != approvals.StatusApproved {
		t.Fatalf("Submit = %+v, %v", r, err)
	}
	a, err := f.store.Decision(ctx, "apr-0001")
	if err != nil || a.ActorID != "user:owner-1" || a.ActorKind != core.ActorHuman ||
		a.Surface != core.SurfaceDashboard || a.Reason != "scope reviewed" || a.Signature.IsZero() {
		t.Fatalf("recorded decision = %+v, %v", a, err)
	}
	if _, err := f.store.Consume(ctx, "apr-0001", subject, policy); err != nil {
		t.Fatalf("an Owner approval must authorize its subject: %v", err)
	}
}

func TestOnlyAnOwnerMayDecide(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		mutate func(*core.Actor)
		want   core.ErrorCode
	}{
		"a reporter":   {func(a *core.Actor) { a.Roles = []core.Role{core.RoleReporter} }, core.CodePermissionDenied},
		"a maintainer": {func(a *core.Actor) { a.Roles = []core.Role{core.RoleMaintainer} }, core.CodePermissionDenied},
		"a coding agent": {func(a *core.Actor) {
			a.Kind, a.Roles = core.ActorAgent, []core.Role{core.RoleAgent}
		}, core.CodePermissionDenied},
		"no role at all":     {func(a *core.Actor) { a.Roles = nil }, core.CodePermissionDenied},
		"an unknown caller":  {func(a *core.Actor) { a.ID = "" }, core.CodeUnauthenticated},
		"an unknown kind":    {func(a *core.Actor) { a.Kind = "bot" }, core.CodeUnauthenticated},
		"an unknown surface": {func(a *core.Actor) { a.Surface = "" }, core.CodeUnauthenticated},
		// An agent that holds or borrows the Owner role still is not the person.
		"an agent using an owner session": {func(a *core.Actor) { a.Kind = core.ActorAgent }, core.CodeStepUpRequired},
		"an owner who never confirmed":    {func(a *core.Actor) { a.StepUpAt = time.Time{} }, core.CodeStepUpRequired},
	}
	for name, tc := range cases {
		f := newFixture(t)
		f.pending(t, nil)
		actor := owner(f.clock.Now())
		tc.mutate(&actor)
		_, err := f.store.Submit(context.Background(), actor, shown(approvals.DecisionApprove))
		wantCode(t, err, tc.want, name)
		if got := f.status(t); got != approvals.StatusPending {
			t.Fatalf("%s: the request became %s", name, got)
		}
		_, err = f.store.Consume(context.Background(), "apr-0001", subject, policy)
		wantCode(t, err, core.CodeApprovalRequired, name+": use after the refused decision")
	}
}

func TestARefusedSubmissionIsAuditedWithItsActor(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pending(t, nil)
	reporter := owner(f.clock.Now())
	reporter.ID, reporter.Roles = "user:reporter-9", []core.Role{core.RoleReporter}
	_, err := f.store.Submit(context.Background(), reporter, shown(approvals.DecisionApprove))
	wantCode(t, err, core.CodePermissionDenied, "reporter")
	denied := f.audit.Find("approvals.decide", core.OutcomeDenied)
	if len(denied) != 1 || denied[0].ActorID != "user:reporter-9" || denied[0].SubjectDigest != subject {
		t.Fatalf("the refused attempt must be audited with its actor, got %+v", denied)
	}
}

func TestASubmissionMustEchoWhatTheApproverWasShown(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		mutate func(*approvals.Submission)
		want   core.ErrorCode
	}{
		"another nonce":          {func(s *approvals.Submission) { s.Nonce = "0000000000000000" }, core.CodeReplayed},
		"no nonce":               {func(s *approvals.Submission) { s.Nonce = "" }, core.CodeReplayed},
		"an edited subject":      {func(s *approvals.Submission) { s.SubjectDigest = other }, core.CodeApprovalRequired},
		"a newer policy":         {func(s *approvals.Submission) { s.PolicyRevision = policy2 }, core.CodeApprovalRequired},
		"an unknown decision":    {func(s *approvals.Submission) { s.Decision = "sure" }, core.CodeInvalidArgument},
		"an unknown request":     {func(s *approvals.Submission) { s.RequestID = "apr-9999" }, core.CodeNotFound},
		"a negative budget cap":  {func(s *approvals.Submission) { s.Conditions.BudgetCap = -1 }, core.CodeInvalidArgument},
		"a request id left out":  {func(s *approvals.Submission) { s.RequestID = "" }, core.CodeInvalidArgument},
		"whitespace for a nonce": {func(s *approvals.Submission) { s.Nonce = strings.Repeat(" ", 16) }, core.CodeReplayed},
	}
	for name, tc := range cases {
		f := newFixture(t)
		f.pending(t, nil)
		s := shown(approvals.DecisionApprove)
		tc.mutate(&s)
		_, err := f.store.Submit(context.Background(), owner(f.clock.Now()), s)
		wantCode(t, err, tc.want, name)
		if got := f.status(t); got != approvals.StatusPending {
			t.Fatalf("%s: the request became %s", name, got)
		}
	}
}

func TestAnOwnerMayRejectOrAskForChanges(t *testing.T) {
	t.Parallel()
	want := map[approvals.Decision]approvals.Status{
		approvals.DecisionReject:         approvals.StatusRejected,
		approvals.DecisionRequestChanges: approvals.StatusChangesRequested,
	}
	for decision, status := range want {
		f := newFixture(t)
		f.pending(t, nil)
		r, err := f.store.Submit(context.Background(), owner(f.clock.Now()), shown(decision))
		if err != nil || r.Status != status {
			t.Fatalf("%s: %+v, %v", decision, r, err)
		}
		_, err = f.store.Consume(context.Background(), "apr-0001", subject, policy)
		wantCode(t, err, core.CodePermissionDenied, string(decision))
	}
}

package approvals_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestADecisionIsStampedSignedAndRecorded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	r := f.decided(t, approvals.DecisionApprove)
	if r.Status != approvals.StatusApproved {
		t.Fatalf("status after approval = %s", r.Status)
	}
	a, err := f.store.Decision(ctx, "apr-0001")
	if err != nil {
		t.Fatal(err)
	}
	if !a.DecidedAt.Equal(f.clock.Now()) || !a.ExpiresAt.Equal(expires) {
		t.Fatalf("decided at %s, expires %s", a.DecidedAt, a.ExpiresAt)
	}
	if a.Signature.KeyID != "approval-key-1" {
		t.Fatalf("the decision must be signed by the approval service, got %+v", a.Signature)
	}
	if err := f.keys.Verify(ctx, a.SigningPayload(), a.Signature); err != nil {
		t.Fatalf("the recorded signature does not cover the recorded decision: %v", err)
	}
	executed := f.audit.Find("approvals.decide", core.OutcomeExecuted)
	if len(executed) != 1 || executed[0].ActorID != "user:owner-1" || executed[0].ActorKind != core.ActorHuman ||
		executed[0].Surface != core.SurfaceDashboard || executed[0].PolicySHA != policy {
		t.Fatalf("the decision must be audited with who decided and where, got %+v", executed)
	}
}

func TestDecidingTwiceIsRefusedAsReplay(t *testing.T) {
	t.Parallel()
	for _, first := range []approvals.Decision{approvals.DecisionApprove, approvals.DecisionReject, approvals.DecisionRequestChanges} {
		f := newFixture(t)
		f.decided(t, first)
		_, err := f.store.Decide(asOwner(validApproval()), validApproval())
		wantCode(t, err, core.CodeReplayed, "second decision after "+string(first))
		if a, _ := f.store.Decision(context.Background(), "apr-0001"); a.Decision != first {
			t.Fatalf("the first decision %s was replaced by %s", first, a.Decision)
		}
	}
}

func TestADecisionWithAForeignNonceIsRefusedAsReplay(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pending(t, nil)
	a := validApproval()
	a.Nonce = "another-requests-nonce"
	_, err := f.store.Decide(asOwner(a), a)
	wantCode(t, err, core.CodeReplayed, "decision carrying another nonce")
	if got := f.status(t); got != approvals.StatusPending {
		t.Fatalf("a refused decision changed the request to %s", got)
	}
	if got := f.audit.Find("approvals.decide", core.OutcomeDenied); len(got) != 1 {
		t.Fatalf("a refused decision must be audited, got %+v", got)
	}
}

func TestADecisionOnALapsedRequestIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pending(t, nil)
	f.clock.Advance(expires.Sub(f.clock.Now()))
	a := validApproval()
	a.StepUpAt = f.clock.Now()
	_, err := f.store.Decide(asOwner(a), a)
	wantCode(t, err, core.CodeExpired, "decision at the expiry instant")
	_, err = f.store.Decision(context.Background(), "apr-0001")
	wantCode(t, err, core.CodeNotFound, "decision of a lapsed request")
}

func TestADecisionForAnotherSubjectOrPolicyIsRefused(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*approvals.Approval){
		"another subject":         func(a *approvals.Approval) { a.SubjectDigest = other },
		"another policy revision": func(a *approvals.Approval) { a.PolicyRevision = policy2 },
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
	}
}

func TestASensitiveRequestNeedsAFreshHumanStepUp(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*approvals.Approval, time.Time){
		"an agent riding an owner session": func(a *approvals.Approval, _ time.Time) { a.ActorKind = core.ActorAgent },
		"a service":                        func(a *approvals.Approval, _ time.Time) { a.ActorKind = core.ActorService },
		"a person who never confirmed":     func(a *approvals.Approval, _ time.Time) { a.StepUpAt = time.Time{} },
		"a stale confirmation": func(a *approvals.Approval, now time.Time) {
			a.StepUpAt = now.Add(-approvals.DefaultStepUpMaxAge - time.Second)
		},
		"a confirmation from the future": func(a *approvals.Approval, now time.Time) { a.StepUpAt = now.Add(time.Second) },
	}
	for name, mutate := range cases {
		f := newFixture(t)
		f.pending(t, nil)
		a := validApproval()
		mutate(&a, f.clock.Now())
		_, err := f.store.Decide(asOwner(a), a)
		wantCode(t, err, core.CodeStepUpRequired, name)
		if got := f.status(t); got != approvals.StatusPending {
			t.Fatalf("%s: the request became %s", name, got)
		}
	}

	// A request that asks for no step-up still records who decided.
	f := newFixture(t)
	f.pending(t, func(r *approvals.Request) { r.StepUp = false })
	a := validApproval()
	a.StepUpAt = time.Time{}
	if _, err := f.store.Decide(asOwner(a), a); err != nil {
		t.Fatalf("a request without step-up refused an ordinary decision: %v", err)
	}
}

func TestAnIncompleteDecisionIsRefused(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*approvals.Approval){
		"no request":       func(a *approvals.Approval) { a.RequestID = "" },
		"unknown decision": func(a *approvals.Approval) { a.Decision = "ok" },
		"negative budget":  func(a *approvals.Approval) { a.Conditions.BudgetCap = -1 },
	}
	for name, mutate := range cases {
		f := newFixture(t)
		f.pending(t, nil)
		a := validApproval()
		mutate(&a)
		_, err := f.store.Decide(asOwner(a), a)
		wantCode(t, err, core.CodeInvalidArgument, name)
	}
}

func TestACallerCannotBackdateSignOrOutliveTheRequest(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.pending(t, nil)
	a := validApproval()
	a.DecidedAt = created.Add(-24 * time.Hour)
	a.ExpiresAt = expires.Add(365 * 24 * time.Hour)
	a.Signature = core.Signature{KeyID: "attacker", Algorithm: "ed25519", Value: []byte("forged")}
	if _, err := f.store.Decide(asOwner(a), a); err != nil {
		t.Fatal(err)
	}
	got, _ := f.store.Decision(ctx, "apr-0001")
	if !got.DecidedAt.Equal(f.clock.Now()) || got.ExpiresAt.After(expires) || got.Signature.KeyID != "approval-key-1" {
		t.Fatalf("caller-supplied time, expiry or signature was kept: %+v", got)
	}

	// An approver may shorten the approval, never lengthen it.
	g := newFixture(t)
	g.pending(t, nil)
	short := validApproval()
	short.ExpiresAt = created.Add(time.Hour)
	if _, err := g.store.Decide(asOwner(short), short); err != nil {
		t.Fatal(err)
	}
	if kept, _ := g.store.Decision(ctx, "apr-0001"); !kept.ExpiresAt.Equal(created.Add(time.Hour)) {
		t.Fatalf("a shorter expiry was not kept: %s", kept.ExpiresAt)
	}
}

func TestADecisionThatCannotBeSignedIsNotRecorded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	store, err := approvals.NewStore(approvals.Config{
		State: f.state, Signer: brokenSigner{}, Verifier: f.keys, Audit: f.audit, Clock: f.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.pending(t, nil)
	if _, err := store.Decide(asOwner(validApproval()), validApproval()); err == nil {
		t.Fatal("an unsigned decision was recorded")
	}
	if got := f.status(t); got != approvals.StatusPending {
		t.Fatalf("the request became %s without a signed decision", got)
	}
}

// brokenSigner fails every signature, as an unavailable key service would.
type brokenSigner struct{}

func (brokenSigner) KeyID() string { return "broken" }

func (brokenSigner) Sign(context.Context, []byte) (core.Signature, error) {
	return core.Signature{}, core.NewError(core.CodeUnavailable, "the signing key is unavailable")
}

func TestADecisionIsRecordedOnlyForTheAuthenticatedOwner(t *testing.T) {
	t.Parallel()
	a := validApproval()
	session := core.Actor{ID: a.ActorID, Kind: a.ActorKind, Roles: []core.Role{core.RoleOwner},
		Surface: a.Surface, StepUpAt: a.StepUpAt}
	as := func(change func(*core.Actor)) context.Context {
		actor := session
		change(&actor)
		return approvals.WithApprover(context.Background(), actor)
	}
	cases := map[string]struct {
		ctx  context.Context
		want core.ErrorCode
	}{
		// Naming the Owner in the fields of a decision proves nothing.
		"nobody was authenticated":   {context.Background(), core.CodeUnauthenticated},
		"an unidentified session":    {as(func(c *core.Actor) { c.ID = "" }), core.CodeUnauthenticated},
		"a session of unknown kind":  {as(func(c *core.Actor) { c.Kind = "bot" }), core.CodeUnauthenticated},
		"someone else's session":     {as(func(c *core.Actor) { c.ID = "user:maintainer-2" }), core.CodePermissionDenied},
		"an agent's session":         {as(func(c *core.Actor) { c.Kind = core.ActorAgent }), core.CodePermissionDenied},
		"another surface":            {as(func(c *core.Actor) { c.Surface = core.SurfaceMCP }), core.CodePermissionDenied},
		"an older identity check":    {as(func(c *core.Actor) { c.StepUpAt = c.StepUpAt.Add(-time.Hour) }), core.CodePermissionDenied},
		"a maintainer":               {as(func(c *core.Actor) { c.Roles = []core.Role{core.RoleMaintainer} }), core.CodePermissionDenied},
		"a session without any role": {as(func(c *core.Actor) { c.Roles = nil }), core.CodePermissionDenied},
	}
	for name, tc := range cases {
		f := newFixture(t)
		f.pending(t, nil)
		_, err := f.store.Decide(tc.ctx, a)
		wantCode(t, err, tc.want, name)
		if got := f.status(t); got != approvals.StatusPending {
			t.Fatalf("%s: the request became %s", name, got)
		}
		_, err = f.store.Decision(context.Background(), a.RequestID)
		wantCode(t, err, core.CodeNotFound, name+": a decision was recorded")
	}
}

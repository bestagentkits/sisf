package intake_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// doctored is an approval store that alters the request and the decision it
// hands over when an approval is spent, as a store with a fault or a
// break-in would. Everything else it does correctly.
type doctored struct {
	*approvals.KVStore
	alter func(*approvals.Request, *approvals.Approval)
}

func (d doctored) ConsumeWith(ctx context.Context, id string, subject core.Digest, policy core.CommitSHA, write approvals.SpendFunc) (approvals.Approval, error) {
	return d.KVStore.ConsumeWith(ctx, id, subject, policy,
		func(ctx context.Context, tx state.Tx, r approvals.Request, a approvals.Approval) error {
			d.alter(&r, &a)
			return write(ctx, tx, r, a)
		})
}

func TestTheLifecycleRefusesReadyOnAnApprovalThatDoesNotHold(t *testing.T) {
	t.Parallel()
	faults := map[string]func(*approvals.Request, *approvals.Approval){
		"decided by an agent":         func(_ *approvals.Request, a *approvals.Approval) { a.ActorKind = core.ActorAgent },
		"decided by a service":        func(_ *approvals.Request, a *approvals.Approval) { a.ActorKind = core.ActorService },
		"not signed":                  func(_ *approvals.Request, a *approvals.Approval) { a.Signature = core.Signature{} },
		"a rejection":                 func(_ *approvals.Request, a *approvals.Approval) { a.Decision = approvals.DecisionReject },
		"about another issue":         func(r *approvals.Request, _ *approvals.Approval) { r.IssueID.Number++ },
		"a decision on another nonce": func(_ *approvals.Request, a *approvals.Approval) { a.Nonce = "0000000000000000" },
		"a decision on other content": func(_ *approvals.Request, a *approvals.Approval) {
			a.SubjectDigest = "sha256:" + a.SubjectDigest[len("sha256:"):][32:] + a.SubjectDigest[len("sha256:"):][:32]
		},
		"under another policy": func(_ *approvals.Request, a *approvals.Approval) { a.PolicyRevision = newerPolicy },
		"still pending":        func(r *approvals.Request, _ *approvals.Approval) { r.Status = approvals.StatusPending },
		"already used":         func(r *approvals.Request, _ *approvals.Approval) { r.Status = approvals.StatusConsumed },
		"dated in the future":  func(_ *approvals.Request, a *approvals.Approval) { a.DecidedAt = a.DecidedAt.Add(time.Hour) },
		"no decision at all":   func(_ *approvals.Request, a *approvals.Approval) { *a = approvals.Approval{} },
		// The factory the step is judged for comes from the policy in force,
		// so a request and a decision that agree on another one prove nothing.
		"requested and decided in another factory": func(r *approvals.Request, a *approvals.Approval) {
			r.FactoryID, a.FactoryID = "another-factory", "another-factory"
		},
		"requested in another factory":  func(r *approvals.Request, _ *approvals.Approval) { r.FactoryID = "another-factory" },
		"a decision in another factory": func(_ *approvals.Request, a *approvals.Approval) { a.FactoryID = "another-factory" },
		"a decision on another issue":   func(_ *approvals.Request, a *approvals.Approval) { a.IssueID.Number++ },
		"a decision on no single issue": func(_ *approvals.Request, a *approvals.Approval) { a.IssueID = core.IssueRef{} },
		"an approval of another action": func(r *approvals.Request, a *approvals.Approval) {
			r.Action, a.Action = "release.publish", "release.publish"
		},
		"a decision for another action":   func(_ *approvals.Request, a *approvals.Approval) { a.Action = "release.publish" },
		"decided before the request":      func(r *approvals.Request, a *approvals.Approval) { a.DecidedAt = r.CreatedAt.Add(-time.Second) },
		"decided after the request ended": func(r *approvals.Request, a *approvals.Approval) { r.ExpiresAt = a.DecidedAt },
	}
	for name, alter := range faults {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newFactoryWith(t, func(store *approvals.KVStore) intake.Approvals { return doctored{store, alter} })
			f.open(member, featureBody, github.LabelReady, github.LabelKindFeature)
			f.reconcile()
			request := f.waiting()
			f.decide(request, approvals.DecisionApprove)
			queued := len(f.state.Outbox())

			_, err := f.service.Reconcile(ctx, issueRef)
			wantCode(t, err, core.CodeGuardFailed, name)
			f.wantState(core.StateApprovalPending)
			// The refused step took nothing with it: the approval is unspent,
			// no scope is handed out and nothing is shown on the issue.
			if got := f.requestFor(request.SubjectDigest); got.Status != approvals.StatusApproved {
				t.Fatalf("the approval became %s", got.Status)
			}
			if _, err := f.service.ApprovedScope(ctx, issueRef); !core.HasCode(err, core.CodeApprovalRequired) {
				t.Fatalf("a scope was handed out: %v", err)
			}
			if len(f.state.Outbox()) != queued {
				t.Fatal("the refused step queued an effect")
			}
		})
	}
}

// retry is the step a retry of the blocked issue asks for.
func (f *factory) retry() core.Transition {
	record, _ := f.state.Record(issueRef)
	return core.Transition{IssueID: issueRef, From: core.StateBlocked, To: core.StateReady, Event: core.EventRetryAuthorized,
		ExpectedVersion: record.Version, IdempotencyKey: "retry/1", ActorID: "user:owner-1"}
}

// permissive is a retry guard that lets the Owner retry any issue, as the
// rules for an issue blocked after a rollback might, and counts the asks.
type permissive struct {
	asked int
	now   func() time.Time
}

func (p *permissive) RetryProof(_ context.Context, t core.Transition) (statemachine.RetryProof, error) {
	p.asked++
	return statemachine.RetryProof{
		IssueID: t.IssueID, Cause: statemachine.BlockRolledBack, BlockedAt: p.now().Add(-time.Hour),
		Actor:    core.Actor{ID: t.ActorID, Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceDashboard},
		Decision: core.AuthzDecision{Effect: core.EffectAllow},
	}, nil
}

func TestARetryCannotReviveAnIssueWhoseScopeWasNotApproved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	outcomes := map[string]func(f *factory, r approvals.Request){
		"rejected": func(f *factory, r approvals.Request) { f.decide(r, approvals.DecisionReject) },
		"expired":  func(f *factory, _ approvals.Request) { f.clock.Advance(intake.DefaultApprovalTTL) },
	}
	for name, outcome := range outcomes {
		f, request := proposed(t)
		outcome(f, request)
		f.reconcile()
		f.wantState(core.StateBlocked)

		// Even a guard that lets the Owner retry anything is not asked.
		next := &permissive{now: f.clock.Now}
		guard := f.service.RetryGuard(next)
		proof, err := guard.RetryProof(ctx, f.retry())
		if !core.HasCode(err, core.CodeGuardFailed) || proof.Decision.Allowed() || proof.Approval != nil || proof.Delegation != nil || next.asked != 0 {
			t.Fatalf("%s: retry proof = %+v, %v (next asked %d times)", name, proof, err, next.asked)
		}
		machine := statemachine.New(f.clock, statemachine.Guards{Retry: guard})
		wantCode(t, machine.Check(ctx, f.retry()), core.CodeGuardFailed, name+": retry through the lifecycle")

		// Nor does the Owner asking intake itself, without a new approval.
		_, err = f.service.Retry(ctx, f.retryBy(f.owner()))
		wantCode(t, err, core.CodeGuardFailed, name+": retry through intake")
		f.wantState(core.StateBlocked)
	}
}

func TestOtherRetriesAreLeftToTheRetryRules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFactory(t)
	f.open(member, completeBody, github.LabelReady, github.LabelKindBug)
	f.reconcile()
	f.wantState(core.StateReady)
	elsewhere := f.retry()
	elsewhere.IssueID.Number = 99

	// An issue intake never blocked for want of an approval, and one it
	// never saw, are judged by the rules behind this one.
	for name, request := range map[string]core.Transition{"taken in": f.retry(), "unknown": elsewhere} {
		next := &permissive{now: f.clock.Now}
		proof, err := f.service.RetryGuard(next).RetryProof(ctx, request)
		if err != nil || !proof.Decision.Allowed() || proof.Cause != statemachine.BlockRolledBack || next.asked != 1 {
			t.Errorf("%s: proof = %+v, %v (next asked %d times)", name, proof, err, next.asked)
		}
		// Without rules behind it the guard proves nothing, and the lifecycle
		// refuses the step.
		alone := f.service.RetryGuard(nil)
		if proof, err := alone.RetryProof(ctx, request); err != nil || proof.Decision.Allowed() || proof.Cause.Valid() {
			t.Errorf("%s: a guard with nothing behind it answered for the retry: %+v, %v", name, proof, err)
		}
		err = statemachine.New(f.clock, statemachine.Guards{Retry: alone}).Check(ctx, request)
		wantCode(t, err, core.CodeGuardFailed, name+": a retry with no rules behind it")
	}
	if _, err := f.service.ApprovalDenial(ctx, issueRef); !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("an issue that was never denied has a denial: %v", err)
	}
}

func TestARetryIsRefusedWhenTheDenialRecordCannotBeRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, request := proposed(t)
	f.decide(request, approvals.DecisionReject)
	f.reconcile()
	// Someone with write access to the database damages the record.
	err := f.state.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, "intake", "issue/"+issueRef.String())
		if err != nil {
			return err
		}
		entry.Value = []byte("{not json")
		_, err = tx.PutKV(ctx, entry)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	next := &permissive{now: f.clock.Now}
	guard := f.service.RetryGuard(next)
	if proof, err := guard.RetryProof(ctx, f.retry()); err == nil || proof.Decision.Allowed() || next.asked != 0 {
		t.Fatalf("a retry passed on an unreadable record: %+v, %v", proof, err)
	}
	if err := statemachine.New(f.clock, statemachine.Guards{Retry: guard}).Check(ctx, f.retry()); err == nil {
		t.Fatal("the lifecycle let the retry through")
	}
	if _, err := f.service.Retry(ctx, f.retryBy(f.owner())); err == nil {
		t.Fatal("intake retried an issue whose record it cannot read")
	}
	f.wantState(core.StateBlocked)
}

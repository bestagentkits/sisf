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

// permissive is a retry guard that lets everyone retry and counts the asks.
type permissive struct{ asked int }

func (p *permissive) AuthorizeRetry(context.Context, core.Transition) (core.AuthzDecision, error) {
	p.asked++
	return core.AuthzDecision{Effect: core.EffectAllow}, nil
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
		next := &permissive{}
		guard := f.service.RetryGuard(next)
		decision, err := guard.AuthorizeRetry(ctx, f.retry())
		if err != nil || decision.Allowed() || decision.Effect != core.EffectRequireApproval || next.asked != 0 {
			t.Fatalf("%s: retry decision = %+v, %v (next asked %d times)", name, decision, err, next.asked)
		}
		machine := statemachine.New(f.clock, statemachine.Guards{Retry: guard})
		wantCode(t, machine.Check(ctx, f.retry()), core.CodeGuardFailed, name+": retry through the lifecycle")
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
		next := &permissive{}
		decision, err := f.service.RetryGuard(next).AuthorizeRetry(ctx, request)
		if err != nil || !decision.Allowed() || next.asked != 1 {
			t.Errorf("%s: decision = %+v, %v (next asked %d times)", name, decision, err, next.asked)
		}
		// Without rules behind it the guard denies.
		alone, err := f.service.RetryGuard(nil).AuthorizeRetry(ctx, request)
		if err != nil || alone.Allowed() {
			t.Errorf("%s: a guard with nothing behind it allowed the retry: %+v, %v", name, alone, err)
		}
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
	next := &permissive{}
	guard := f.service.RetryGuard(next)
	if decision, err := guard.AuthorizeRetry(ctx, f.retry()); err == nil || decision.Allowed() || next.asked != 0 {
		t.Fatalf("a retry passed on an unreadable record: %+v, %v", decision, err)
	}
	if err := statemachine.New(f.clock, statemachine.Guards{Retry: guard}).Check(ctx, f.retry()); err == nil {
		t.Fatal("the lifecycle let the retry through")
	}
}

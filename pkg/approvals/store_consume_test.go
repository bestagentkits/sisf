package approvals_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

func TestAnApprovalIsSpentExactlyOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.decided(t, approvals.DecisionApprove)
	a, err := f.store.Consume(ctx, "apr-0001", subject, policy)
	if err != nil || a.Decision != approvals.DecisionApprove || a.ActorID != "user:owner-1" {
		t.Fatalf("Consume = %+v, %v", a, err)
	}
	if got := f.status(t); got != approvals.StatusConsumed {
		t.Fatalf("status after use = %s", got)
	}
	_, err = f.store.Consume(ctx, "apr-0001", subject, policy)
	wantCode(t, err, core.CodeReplayed, "second use")
	_, err = f.store.Decide(asOwner(validApproval()), validApproval())
	wantCode(t, err, core.CodeReplayed, "deciding a used request again")
	if got := f.audit.Find("approvals.consume", core.OutcomeExecuted); len(got) != 1 {
		t.Fatalf("one use must leave one audit record, got %+v", got)
	}
	if got := f.audit.Find("approvals.consume", core.OutcomeDenied); len(got) != 1 {
		t.Fatalf("the replay must be audited as denied, got %+v", got)
	}
}

func TestConcurrentConsumersSpendAnApprovalOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.decided(t, approvals.DecisionApprove)
	const consumers = 16
	var wg sync.WaitGroup
	results := make([]error, consumers)
	for i := range consumers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = f.store.Consume(context.Background(), "apr-0001", subject, policy)
		}()
	}
	wg.Wait()
	spent := 0
	for _, err := range results {
		switch {
		case err == nil:
			spent++
		case !core.HasCode(err, core.CodeReplayed):
			t.Fatalf("a losing consumer must see a replay, got %v", err)
		}
	}
	if spent != 1 {
		t.Fatalf("the approval was spent %d times", spent)
	}
}

func TestSingleUseSurvivesARestartOfTheStore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.decided(t, approvals.DecisionApprove)
	if _, err := f.store.Consume(ctx, "apr-0001", subject, policy); err != nil {
		t.Fatal(err)
	}
	restarted := f.open(t)
	_, err := restarted.Consume(ctx, "apr-0001", subject, policy)
	wantCode(t, err, core.CodeReplayed, "use after restart")
}

func TestAnApprovalIsNotSpentOnAnEditedSubjectOrAChangedPolicy(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.decided(t, approvals.DecisionApprove)
	_, err := f.store.Consume(ctx, "apr-0001", other, policy)
	wantCode(t, err, core.CodeApprovalRequired, "subject edited after approval")
	_, err = f.store.Consume(ctx, "apr-0001", subject, policy2)
	wantCode(t, err, core.CodeApprovalRequired, "policy changed after approval")
	_, err = f.store.Consume(ctx, "apr-0001", "", policy)
	wantCode(t, err, core.CodeApprovalRequired, "no subject")
	if got := f.status(t); got != approvals.StatusApproved {
		t.Fatalf("a refused use changed the request to %s", got)
	}
	// The refusals did not burn it: the approved subject can still be acted on.
	if _, err := f.store.Consume(ctx, "apr-0001", subject, policy); err != nil {
		t.Fatalf("the approval no longer covers what was approved: %v", err)
	}
}

func TestAnUndecidedRejectedOrReturnedRequestAuthorizesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	f.pending(t, nil)
	_, err := f.store.Consume(ctx, "apr-0001", subject, policy)
	wantCode(t, err, core.CodeApprovalRequired, "undecided request")

	for _, decision := range []approvals.Decision{approvals.DecisionReject, approvals.DecisionRequestChanges} {
		g := newFixture(t)
		g.decided(t, decision)
		_, err := g.store.Consume(ctx, "apr-0001", subject, policy)
		wantCode(t, err, core.CodePermissionDenied, string(decision))
	}
}

func TestAnExpiredApprovalIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	lapsedRequest := newFixture(t)
	lapsedRequest.pending(t, nil)
	lapsedRequest.clock.Advance(48 * time.Hour)
	_, err := lapsedRequest.store.Consume(ctx, "apr-0001", subject, policy)
	wantCode(t, err, core.CodeExpired, "request that lapsed undecided")

	lapsedApproval := newFixture(t)
	lapsedApproval.decided(t, approvals.DecisionApprove)
	lapsedApproval.clock.Advance(expires.Sub(lapsedApproval.clock.Now()))
	_, err = lapsedApproval.store.Consume(ctx, "apr-0001", subject, policy)
	wantCode(t, err, core.CodeExpired, "approval at its expiry instant")

	conditional := newFixture(t)
	conditional.pending(t, nil)
	a := validApproval()
	a.Conditions.ExpiresAt = created.Add(30 * time.Minute)
	if _, err := conditional.store.Decide(asOwner(a), a); err != nil {
		t.Fatal(err)
	}
	conditional.clock.Advance(time.Hour)
	_, err = conditional.store.Consume(ctx, "apr-0001", subject, policy)
	wantCode(t, err, core.CodeExpired, "approval whose expiry condition passed")
}

// rewriteDecision edits the stored decision record behind the store's back,
// as someone with write access to the database could.
func rewriteDecision(t *testing.T, f fixture, edit func(*approvals.Approval)) {
	t.Helper()
	err := f.state.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, "approvals", "decision/apr-0001")
		if err != nil {
			return err
		}
		var a approvals.Approval
		if err := json.Unmarshal(entry.Value, &a); err != nil {
			return err
		}
		edit(&a)
		if entry.Value, err = json.Marshal(a); err != nil {
			return err
		}
		_, err = tx.PutKV(ctx, entry)
		return err
	})
	if err != nil {
		t.Fatalf("decision record could not be rewritten: %v", err)
	}
}

func TestAnEditedDecisionRecordFailsItsSignature(t *testing.T) {
	t.Parallel()
	edits := map[string]func(*approvals.Approval){
		"expiry pushed out":       func(a *approvals.Approval) { a.ExpiresAt = a.ExpiresAt.Add(365 * 24 * time.Hour) },
		"approver replaced":       func(a *approvals.Approval) { a.ActorID = "user:someone-else" },
		"budget condition lifted": func(a *approvals.Approval) { a.Conditions.BudgetCap = 0 },
		"moved to another subject": func(a *approvals.Approval) {
			a.SubjectDigest = other
		},
		"signature removed": func(a *approvals.Approval) { a.Signature = core.Signature{} },
	}
	for name, edit := range edits {
		f := newFixture(t)
		f.pending(t, nil)
		a := validApproval()
		a.Conditions.BudgetCap = 5_000_000
		if _, err := f.store.Decide(asOwner(a), a); err != nil {
			t.Fatal(err)
		}
		rewriteDecision(t, f, edit)
		for _, target := range []core.Digest{subject, other} {
			_, err := f.store.Consume(context.Background(), "apr-0001", target, policy)
			wantCode(t, err, core.CodeSignatureInvalid, name)
		}
	}
}

func TestARejectionRewrittenAsApprovalFailsItsSignature(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.decided(t, approvals.DecisionReject)
	rewriteDecision(t, f, func(a *approvals.Approval) { a.Decision = approvals.DecisionApprove })
	_, err := f.store.Consume(context.Background(), "apr-0001", subject, policy)
	wantCode(t, err, core.CodeSignatureInvalid, "forged approval")
}

func TestARevokedSigningKeyVoidsTheApproval(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.decided(t, approvals.DecisionApprove)
	f.keys.Revoke()
	_, err := f.store.Consume(context.Background(), "apr-0001", subject, policy)
	wantCode(t, err, core.CodeSignatureInvalid, "approval signed by a revoked key")
	if got := f.status(t); got != approvals.StatusApproved {
		t.Fatalf("status = %s", got)
	}
}

func TestAUseThatCannotBeAnnouncedSpendsNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.decided(t, approvals.DecisionApprove)
	f.audit.FailWith(core.NewError(core.CodeUnavailable, "audit log is down"))
	_, err := f.store.Consume(ctx, "apr-0001", subject, policy)
	wantCode(t, err, core.CodeUnavailable, "use without an audit record")
	if got := f.status(t); got != approvals.StatusApproved {
		t.Fatalf("an unaudited attempt changed the request to %s", got)
	}
	// Once the log is back the approval is still good for its one use.
	f.audit.FailWith(nil)
	if _, err := f.store.Consume(ctx, "apr-0001", subject, policy); err != nil {
		t.Fatalf("use after the log came back: %v", err)
	}
	if got := f.audit.Find("approvals.consume", core.OutcomeAllowed); len(got) != 1 {
		t.Fatalf("the use must be announced before it happens, got %+v", got)
	}
}

func TestAUseWhoseFinalAuditRecordFailsIsNotReportedAsDone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.decided(t, approvals.DecisionApprove)
	// The log takes the announcement and then goes down.
	f.audit.FailAfter(1, core.NewError(core.CodeUnavailable, "audit log is down"))
	_, err := f.store.Consume(ctx, "apr-0001", subject, policy)
	wantCode(t, err, core.CodeUnavailable, "use whose final record is missing")
	f.audit.FailWith(nil)
	// The approval is burnt, not handed out a second time.
	_, err = f.store.Consume(ctx, "apr-0001", subject, policy)
	wantCode(t, err, core.CodeReplayed, "use after the unaudited attempt")
}

func TestALostWriteRaceIsRetriedAndStillSpendsOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.decided(t, approvals.DecisionApprove)
	f.state.ConflictNextWrites(1)
	if _, err := f.store.Consume(ctx, "apr-0001", subject, policy); err != nil {
		t.Fatalf("one lost race must be retried: %v", err)
	}
	_, err := f.store.Consume(ctx, "apr-0001", subject, policy)
	wantCode(t, err, core.CodeReplayed, "second use")

	// A writer that keeps losing reports the conflict instead of looping.
	g := newFixture(t)
	g.decided(t, approvals.DecisionApprove)
	g.state.ConflictNextWrites(100)
	_, err = g.store.Consume(ctx, "apr-0001", subject, policy)
	wantCode(t, err, core.CodeConflict, "persistent conflict")
}

package approvals_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// noteWritten is the caller's write of these tests: one document that says
// the permitted step was taken.
func noteWritten(ctx context.Context, tx state.Tx, _ approvals.Request, _ approvals.Approval) error {
	_, err := tx.PutKV(ctx, state.KVEntry{Namespace: "test", Key: "step", Value: []byte(`"taken"`)})
	return err
}

// stepTaken reports whether the caller's write is in the store.
func stepTaken(t *testing.T, f fixture) bool {
	t.Helper()
	taken := false
	err := f.state.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		_, err := tx.GetKV(ctx, "test", "step")
		taken = err == nil
		if core.HasCode(err, core.CodeNotFound) {
			return nil
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return taken
}

func TestAnApprovalIsSpentTogetherWithTheStepItPermits(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.decided(t, approvals.DecisionApprove)
	var seen approvals.Request
	var decision approvals.Approval
	spent, err := f.store.ConsumeWith(ctx, "apr-0001", subject, policy,
		func(ctx context.Context, tx state.Tx, r approvals.Request, a approvals.Approval) error {
			seen, decision = r, a
			return noteWritten(ctx, tx, r, a)
		})
	if err != nil {
		t.Fatalf("ConsumeWith: %v", err)
	}
	// The write is handed the request while it is still approved, with the
	// decision whose signature was just verified.
	if seen.Status != approvals.StatusApproved || seen.ID != "apr-0001" {
		t.Fatalf("the write saw request %+v", seen)
	}
	if err := f.keys.Verify(ctx, decision.SigningPayload(), decision.Signature); err != nil || decision.RequestID != spent.RequestID {
		t.Fatalf("the write saw decision %+v: %v", decision, err)
	}
	if got := f.status(t); got != approvals.StatusConsumed || !stepTaken(t, f) {
		t.Fatalf("status %s, step taken %v", got, stepTaken(t, f))
	}
	_, err = f.store.ConsumeWith(ctx, "apr-0001", subject, policy, noteWritten)
	wantCode(t, err, core.CodeReplayed, "second use")
}

func TestAStepThatFailsLeavesTheApprovalUnspent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.decided(t, approvals.DecisionApprove)
	refusal := core.NewError(core.CodeGuardFailed, "the step was refused")
	_, err := f.store.ConsumeWith(ctx, "apr-0001", subject, policy,
		func(ctx context.Context, tx state.Tx, r approvals.Request, a approvals.Approval) error {
			// What was written before the refusal must not survive it.
			if err := noteWritten(ctx, tx, r, a); err != nil {
				return err
			}
			return refusal
		})
	if !errors.Is(err, refusal) {
		t.Fatalf("the caller must get its own error back, got %v", err)
	}
	if got := f.status(t); got != approvals.StatusApproved || stepTaken(t, f) {
		t.Fatalf("after a refused step: status %s, step taken %v", got, stepTaken(t, f))
	}
	if got := f.audit.Find("approvals.consume", core.OutcomeFailed); len(got) != 1 || got[0].Details["code"] != string(core.CodeGuardFailed) {
		t.Fatalf("the announced use that did not happen must be audited as failed, got %+v", got)
	}
	// The approval is still good for the step it was given for.
	if _, err := f.store.ConsumeWith(ctx, "apr-0001", subject, policy, noteWritten); err != nil {
		t.Fatalf("use after the refused step: %v", err)
	}
}

func TestNoStepIsWrittenOnAnApprovalThatDoesNotHold(t *testing.T) {
	t.Parallel()
	never := func(context.Context, state.Tx, approvals.Request, approvals.Approval) error {
		t.Error("the write ran on an approval that does not hold")
		return nil
	}
	cases := map[string]struct {
		prepare func(t *testing.T, f fixture)
		subject core.Digest
		policy  core.CommitSHA
		want    core.ErrorCode
	}{
		"still pending":     {func(t *testing.T, f fixture) { f.pending(t, nil) }, subject, policy, core.CodeApprovalRequired},
		"rejected":          {func(t *testing.T, f fixture) { f.decided(t, approvals.DecisionReject) }, subject, policy, core.CodePermissionDenied},
		"an edited subject": {func(t *testing.T, f fixture) { f.decided(t, approvals.DecisionApprove) }, other, policy, core.CodeApprovalRequired},
		"a newer policy":    {func(t *testing.T, f fixture) { f.decided(t, approvals.DecisionApprove) }, subject, policy2, core.CodeApprovalRequired},
		"a forged decision": {func(t *testing.T, f fixture) {
			f.decided(t, approvals.DecisionReject)
			rewriteDecision(t, f, func(a *approvals.Approval) { a.Decision = approvals.DecisionApprove })
		}, subject, policy, core.CodeSignatureInvalid},
		"a revoked key": {func(t *testing.T, f fixture) {
			f.decided(t, approvals.DecisionApprove)
			f.keys.Revoke()
		}, subject, policy, core.CodeSignatureInvalid},
	}
	for name, tc := range cases {
		f := newFixture(t)
		tc.prepare(t, f)
		_, err := f.store.ConsumeWith(context.Background(), "apr-0001", tc.subject, tc.policy, never)
		wantCode(t, err, tc.want, name)
	}
	f := newFixture(t)
	f.decided(t, approvals.DecisionApprove)
	_, err := f.store.ConsumeWith(context.Background(), "apr-0001", subject, policy, nil)
	wantCode(t, err, core.CodeInvalidArgument, "no write")
	if got := f.status(t); got != approvals.StatusApproved {
		t.Fatalf("status = %s", got)
	}
}

func TestAStepThatLosesARaceIsLeftToItsCaller(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.decided(t, approvals.DecisionApprove)
	calls := 0
	f.state.ConflictNextWrites(1)
	_, err := f.store.ConsumeWith(ctx, "apr-0001", subject, policy,
		func(ctx context.Context, tx state.Tx, r approvals.Request, a approvals.Approval) error {
			calls++
			return noteWritten(ctx, tx, r, a)
		})
	wantCode(t, err, core.CodeConflict, "lost race")
	// The write rests on what its caller read, so it is not run again here.
	if calls != 1 || f.status(t) != approvals.StatusApproved || stepTaken(t, f) {
		t.Fatalf("calls %d, status %s, step taken %v", calls, f.status(t), stepTaken(t, f))
	}
}

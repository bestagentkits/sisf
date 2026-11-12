package wiring

import (
	"context"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Retry sends a Blocked issue back to Ready on the Owner's request.
//
// An issue intake blocked because its scope was not approved goes through
// intake's retry, which needs an approval of the scope given after the
// block, or proof that the scope now lies inside the delegation. When
// neither exists the Owner is asked again: intake files a fresh approval
// request for exactly the blocked scope, and the answer is APPROVAL_REQUIRED
// naming it. Approving that request and retrying again lifts the block. So
// the retry surface is what files the new request, and nothing is filed
// unless someone asked for a retry.
//
// Any other block is judged by the lifecycle's retry guard, with intake's
// rule in front of it; no other cause of a block has a retry rule yet, so
// such a retry is refused.
func (c *Control) Retry(ctx context.Context, issue core.IssueRef, actor core.Actor) (intake.Result, error) {
	decision := core.AuthzDecision{Effect: core.EffectAllow, Reason: "the Owner asked for the retry"}
	if !actor.HasRole(core.RoleOwner) {
		decision = core.AuthzDecision{Effect: core.EffectDeny, Reason: "only the Owner retries a blocked issue"}
	}
	result, err := c.Intake.Retry(ctx, intake.RetryRequest{Issue: issue, Actor: actor, Decision: decision})
	switch {
	case err == nil:
		return result, nil
	case core.HasCode(err, core.CodeNotFound):
		return result, c.guardedRetry(ctx, issue, actor)
	case !core.HasCode(err, core.CodeGuardFailed) || decision.Effect != core.EffectAllow:
		return result, err
	}
	request, askErr := c.Intake.RequestRetryApproval(ctx, issue)
	switch {
	case core.HasCode(askErr, core.CodeConflict):
		// The scope needs no approval; the refusal had another cause.
		return result, err
	case askErr != nil:
		return result, askErr
	}
	return result, core.NewError(core.CodeApprovalRequired,
		"the scope needs the Owner's approval again before the issue can be retried",
		core.NextAction{Action: "approvals.decide", Description: "decide approval request " + request.ID + ", then retry"})
}

// guardedRetry puts a retry of a block intake does not own before the
// lifecycle machine, with intake's retry guard in front of the rest.
func (c *Control) guardedRetry(ctx context.Context, issue core.IssueRef, actor core.Actor) error {
	var record core.StateRecord
	err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		record, err = tx.GetRecord(ctx, issue)
		return err
	})
	if err != nil {
		return err
	}
	step := core.Transition{
		IssueID: issue, From: record.State, To: core.StateReady, Event: core.EventRetryAuthorized,
		ExpectedVersion: record.Version, IdempotencyKey: "retry/" + issue.String(), Attempt: record.Attempt,
		RunID: record.RunID, ActorID: actor.ID, Reason: "the Owner asked for a retry",
	}
	machine := statemachine.New(c.cfg.Clock, statemachine.Guards{Retry: c.Intake.RetryGuard(nil)})
	if err := machine.Check(ctx, step); err != nil {
		return err
	}
	return c.cfg.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := tx.Transition(ctx, step)
		return err
	})
}

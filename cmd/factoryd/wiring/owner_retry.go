package wiring

import (
	"context"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
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
// An issue in Developing whose candidate build the supervisor held after
// repeated outages is not Blocked: the lifecycle has no step from
// Developing to Blocked. The Owner's retry of such an issue resumes the
// build instead, and the supervisor tries it again at once. Likewise an
// issue in ReleaseCandidate whose release the supervisor held: the retry
// lets the release start over, and the supervisor releases the candidate
// again in its next round. A refused release is never started over.
//
// Any other block is judged by the lifecycle's retry guard, with intake's
// rule in front of it; no other cause of a block has a retry rule yet, so
// such a retry is refused.
func (c *Control) Retry(ctx context.Context, issue core.IssueRef, actor core.Actor) (intake.Result, error) {
	if result, held, err := c.resumeHeldBuild(ctx, issue, actor); held {
		return result, err
	}
	if result, held, err := c.resumeHeldRelease(ctx, issue, actor); held {
		return result, err
	}
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

// resumeHeldRelease lets the held release of issue's current attempt start
// over on the Owner's retry. held is false when the issue does not wait in
// ReleaseCandidate on a held or refused release.
func (c *Control) resumeHeldRelease(ctx context.Context, issue core.IssueRef, actor core.Actor) (result intake.Result, held bool, err error) {
	err = c.cfg.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		record, err := tx.GetRecord(ctx, issue)
		if err != nil || record.State != core.StateReleaseCandidate {
			return nil
		}
		if _, err := handoff.ReadReleaseHold(ctx, tx, record.RunID, record.Attempt); core.HasCode(err, core.CodeNotFound) {
			return nil
		} else if err != nil {
			held = true
			return err
		}
		held = true
		result = intake.Result{Issue: issue, State: record.State}
		if !actor.HasRole(core.RoleOwner) {
			return core.NewError(core.CodePermissionDenied, "only the Owner resumes a held release")
		}
		return handoff.ResumeHeldRelease(ctx, tx, record.RunID, record.Attempt, actor.ID, c.cfg.Clock.Now().UTC())
	})
	return result, held, err
}

// resumeHeldBuild resumes the held build of issue's current attempt on the
// Owner's retry. held is false when the issue has no held build, and the
// retry then goes on as a retry of a block.
func (c *Control) resumeHeldBuild(ctx context.Context, issue core.IssueRef, actor core.Actor) (result intake.Result, held bool, err error) {
	err = c.cfg.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		record, err := tx.GetRecord(ctx, issue)
		if err != nil || record.State != core.StateDeveloping {
			return nil
		}
		outage, _, err := handoff.ReadBuildOutage(ctx, tx, record.RunID, record.Attempt)
		if err != nil || !outage.Held {
			return nil
		}
		held = true
		result = intake.Result{Issue: issue, State: record.State}
		if !actor.HasRole(core.RoleOwner) {
			return core.NewError(core.CodePermissionDenied, "only the Owner resumes a held build")
		}
		return handoff.ResumeHeldBuild(ctx, tx, record.RunID, record.Attempt, actor.ID, c.cfg.Clock.Now().UTC())
	})
	return result, held, err
}

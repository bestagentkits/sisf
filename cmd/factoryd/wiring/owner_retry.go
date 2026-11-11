package wiring

import (
	"context"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/github"
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
// repeated outages is not Blocked: its patch is good and only waits for
// the outage to end. The Owner's retry of such an issue resumes the build
// instead, and the supervisor tries it again at once. Likewise an
// issue in ReleaseCandidate whose release the supervisor held: the retry
// lets the release start over, and the supervisor releases the candidate
// again in its next round. A refused release is never started over.
//
// An issue the control zone blocked because its run stopped without a
// candidate (handoff.RunStop) returns to Ready on the Owner's retry, where
// a new run claims it. After a run that used its attempts the scope was
// approved and still is, so the retry needs no further proof than that the
// Owner asked. After a run whose policy was superseded the scope is judged
// under the policy in force first, by intake: it is pinned to that policy
// when it lies inside the delegation or the Owner approved it under that
// policy since, and otherwise the Owner is asked, as for a scope that was
// not approved. A run that stopped and is not blocked yet is blocked first,
// so the retry need not wait for the next pass.
//
// Any other block is judged by the lifecycle's retry guard, with intake's
// rule in front of it; a block whose cause has no retry rule, such as a
// release that was rolled back, is refused.
func (c *Control) Retry(ctx context.Context, issue core.IssueRef, actor core.Actor) (intake.Result, error) {
	if _, err := c.blockStoppedRun(ctx, issue); err != nil && !core.HasCode(err, core.CodeLeaseHeld) && !core.HasCode(err, core.CodeNotFound) {
		return intake.Result{Issue: issue}, err
	}
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
	retry := intake.RetryRequest{Issue: issue, Actor: actor, Decision: decision}
	ask := c.Intake.RequestRetryApproval
	superseded, blockedAt, err := c.supersededAt(ctx, issue)
	if err != nil {
		return intake.Result{Issue: issue}, err
	}
	var result intake.Result
	if superseded {
		ask = c.Intake.RequestSupersededScopeApproval
		result, err = c.Intake.RetryUnderPolicyInForce(ctx, retry, blockedAt)
	} else {
		result, err = c.Intake.Retry(ctx, retry)
	}
	switch {
	case err == nil:
		return result, nil
	case core.HasCode(err, core.CodeNotFound) && !superseded:
		return result, c.guardedRetry(ctx, issue, actor)
	case !core.HasCode(err, core.CodeGuardFailed) || decision.Effect != core.EffectAllow:
		return result, err
	}
	request, askErr := ask(ctx, issue)
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
// lifecycle machine, with intake's retry guard in front of the rest. An
// issue can be blocked and retried more than once, so the version it is
// retried at tells the retries apart.
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
		ExpectedVersion: record.Version, IdempotencyKey: fmt.Sprintf("retry/%s/%d", issue, record.Version), Attempt: record.Attempt,
		RunID: record.RunID, ActorID: actor.ID, Reason: "the Owner asked for a retry",
	}
	machine := statemachine.New(c.cfg.Clock, statemachine.Guards{Retry: c.Intake.RetryGuard(c.stoppedRunRetry(actor))})
	if err := machine.Check(ctx, step); err != nil {
		return err
	}
	err = c.cfg.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := tx.Transition(ctx, step)
		return err
	})
	if err != nil {
		return err
	}
	// The issue is Ready again; the next claim sets its lifecycle label,
	// and until then it must not read as blocked.
	unblock := editIntent{remove: []string{github.LabelBlocked}}
	if err := c.editIssue(ctx, issue, unblock, func(ctx context.Context) error {
		err := c.cfg.GitHub.RemoveLabel(ctx, issue, github.LabelBlocked)
		if core.HasCode(err, core.CodeNotFound) {
			return nil
		}
		return err
	}); err != nil {
		c.log.Warn("the blocked label of a retried issue could not be removed; the next claim replaces it",
			"issue", issue.String(), "code", string(core.CodeOf(err)))
	}
	return nil
}

// supersededAt reports whether issue is blocked because the policy its run
// was coded under was superseded, and when it was blocked, from the record
// written with the block for the run the issue's record names.
func (c *Control) supersededAt(ctx context.Context, issue core.IssueRef) (bool, time.Time, error) {
	var stop handoff.RunStop
	err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		record, err := tx.GetRecord(ctx, issue)
		if err != nil || record.State != core.StateBlocked || record.RunID == "" {
			return err
		}
		stop, err = handoff.ReadRunStop(ctx, tx, record.RunID)
		return err
	})
	if core.HasCode(err, core.CodeNotFound) {
		return false, time.Time{}, nil
	}
	superseded := err == nil && stop.IssueID == issue && stop.Cause == statemachine.BlockPolicySuperseded
	return superseded, stop.BlockedAt, err
}

// stoppedRunRetry is the retry guard of an issue the control zone blocked
// because its run stopped. Why and when it was blocked are read from the
// record written with the block, for the run the step names; an issue
// blocked for any other reason has no such record of its current run, and
// the guard then proves nothing. Only the Owner is allowed.
func (c *Control) stoppedRunRetry(actor core.Actor) statemachine.RetryGuard {
	return statemachine.RetryGuardFunc(func(ctx context.Context, t core.Transition) (statemachine.RetryProof, error) {
		var stop handoff.RunStop
		err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
			var err error
			stop, err = handoff.ReadRunStop(ctx, tx, t.RunID)
			return err
		})
		switch {
		case core.HasCode(err, core.CodeNotFound) || (err == nil && stop.IssueID != t.IssueID):
			return statemachine.RetryProof{}, nil
		case err != nil:
			return statemachine.RetryProof{}, err
		}
		decision := core.AuthzDecision{Effect: core.EffectAllow, Reason: "the Owner asked for the retry"}
		if !actor.HasRole(core.RoleOwner) {
			decision = core.AuthzDecision{Effect: core.EffectDeny, Reason: "only the Owner retries a blocked issue"}
		}
		return statemachine.RetryProof{IssueID: stop.IssueID, Cause: stop.Cause, BlockedAt: stop.BlockedAt, Actor: actor, Decision: decision}, nil
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
		if err != nil || !outage.Held || outage.Reason == handoff.HeldPolicySuperseded {
			// A build held over a superseded policy is never resumed: its
			// issue is blocked instead.
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

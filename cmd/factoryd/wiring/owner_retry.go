package wiring

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
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
// issue in ReleaseCandidate whose release the supervisor held before
// anything was deployed: the retry lets the release start over, and the
// supervisor releases the candidate again in its next round.
//
// An issue the control zone blocked because its run stopped without a
// candidate, or because the supervisor refused, rolled back or could not
// finish the run's release (handoff.RunStop), returns to Ready on the
// Owner's retry, where a new run claims it and codes, judges and releases
// it afresh. After a run that used its attempts, and after any release,
// the scope was approved and still is, so the retry needs no further proof
// than that the Owner asked. A rollback recorded before its cause was kept
// is read from its recovery report. After a run whose policy was superseded the scope is judged
// under the policy in force first, by intake: it is pinned to that policy
// when it lies inside the delegation or the Owner approved it under that
// policy since, and otherwise the Owner is asked, as for a scope that was
// not approved. The same holds when the Owner put another policy in force
// after the run stopped, whatever stopped it: a run claimed with a scope of
// the earlier policy would be coded and then stopped as superseded, so the
// retry has the scope judged under the policy in force instead of carrying
// the earlier permission over. A run that stopped and is not blocked yet is
// blocked first, so the retry need not wait for the next pass.
//
// Any other block is judged by the lifecycle's retry guard, with intake's
// rule in front of it; a block whose cause has no retry rule is refused.
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
	// rejudge is whether the scope goes before the policy in force first.
	rejudge := superseded
	if !superseded {
		if rejudge, blockedAt, err = c.stoppedUnderEarlierPolicy(ctx, issue); err != nil {
			return intake.Result{Issue: issue}, err
		}
	}
	var result intake.Result
	if rejudge {
		result, err = c.Intake.RetryUnderPolicyInForce(ctx, retry, blockedAt)
		// A block that was not over a superseded policy and that intake
		// does not take as a scope to judge again is retried as before.
		rejudge = superseded || !core.HasCode(err, core.CodeNotFound)
	}
	if rejudge {
		ask = c.Intake.RequestSupersededScopeApproval
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

// stoppedUnderEarlierPolicy reports whether issue is blocked for a run that
// stopped, with a scope that names another policy than the one in force,
// and when it was blocked. The block is read as the retry guard reads it:
// from the record written with it, or from the recovery report of a
// rollback. An issue that is not blocked this way, has no approved scope,
// or whose scope names the policy in force, is not one.
func (c *Control) stoppedUnderEarlierPolicy(ctx context.Context, issue core.IssueRef) (bool, time.Time, error) {
	var stop handoff.RunStop
	var scope intake.SoW
	err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		record, err := tx.GetRecord(ctx, issue)
		if err != nil {
			return err
		}
		if record.State != core.StateBlocked || record.RunID == "" {
			return core.NewError(core.CodeNotFound, "the issue is not blocked for a run")
		}
		stop, err = handoff.ReadRunStop(ctx, tx, record.RunID)
		if core.HasCode(err, core.CodeNotFound) {
			stop, err = rolledBackBefore(ctx, tx, core.Transition{RunID: record.RunID})
		}
		if err != nil {
			return err
		}
		scope, err = intake.ReadApprovedScope(ctx, tx, issue)
		if core.HasCode(err, core.CodeApprovalRequired) {
			return core.NewError(core.CodeNotFound, "the issue has no approved scope")
		}
		return err
	})
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return false, time.Time{}, nil
	case err != nil:
		return false, time.Time{}, err
	case stop.IssueID != issue || !stop.Cause.Valid():
		// Not a block the retry guard would accept; it is left to refuse it.
		return false, time.Time{}, nil
	}
	inForce, err := c.Baselines.Current(ctx)
	if err != nil {
		return false, time.Time{}, err
	}
	return scope.PolicyRevision != inForce.Revision, stop.BlockedAt, nil
}

// stoppedRunRetry is the retry guard of an issue the control zone blocked
// for its run: the run stopped, or its release was refused or undone. Why
// and when it was blocked are read from the record written with the block,
// for the run the step names; an issue blocked for any other reason has no
// such record of its current run, and the guard then proves nothing. Only
// the Owner is allowed.
func (c *Control) stoppedRunRetry(actor core.Actor) statemachine.RetryGuard {
	return statemachine.RetryGuardFunc(func(ctx context.Context, t core.Transition) (statemachine.RetryProof, error) {
		var stop handoff.RunStop
		err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
			var err error
			stop, err = handoff.ReadRunStop(ctx, tx, t.RunID)
			if core.HasCode(err, core.CodeNotFound) {
				stop, err = rolledBackBefore(ctx, tx, t)
			}
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

// rolledBackBefore is the record of a block an earlier release wrote when
// it rolled the run of t back without keeping the cause: its recovery
// report, kept in the transaction that blocked the issue. NOT_FOUND when
// the run has none.
func rolledBackBefore(ctx context.Context, tx state.Tx, t core.Transition) (handoff.RunStop, error) {
	entry, err := tx.GetKV(ctx, release.RecoveryNamespace, release.RecoveryKey(t.RunID))
	if err != nil {
		return handoff.RunStop{}, err
	}
	var report release.RecoveryReport
	if json.Unmarshal(entry.Value, &report) != nil || report.RunID != t.RunID || report.At.IsZero() {
		return handoff.RunStop{}, core.NewError(core.CodeTampered, "the stored recovery report of run "+string(t.RunID)+" cannot be read")
	}
	cause := report.Cause
	if cause == "" {
		cause = statemachine.BlockRolledBack
	}
	return handoff.RunStop{IssueID: report.IssueID, RunID: report.RunID, Cause: cause, Release: report.Release,
		RestoredTo: report.RestoredTo, BlockedAt: report.At}, nil
}

// resumeHeldRelease lets the held release of issue's current attempt start
// over on the Owner's retry. held is false when the issue does not wait in
// ReleaseCandidate on a held release. A record that cannot be read is an
// error, never a reason to go on as if nothing were held.
func (c *Control) resumeHeldRelease(ctx context.Context, issue core.IssueRef, actor core.Actor) (result intake.Result, held bool, err error) {
	err = c.cfg.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		record, err := tx.GetRecord(ctx, issue)
		if core.HasCode(err, core.CodeNotFound) || (err == nil && record.State != core.StateReleaseCandidate) {
			return nil
		}
		if err != nil {
			held = true
			return err
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
// retry then goes on as a retry of a block. A record that cannot be read is
// an error, never a reason to go on as if nothing were held.
func (c *Control) resumeHeldBuild(ctx context.Context, issue core.IssueRef, actor core.Actor) (result intake.Result, held bool, err error) {
	err = c.cfg.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		record, err := tx.GetRecord(ctx, issue)
		if core.HasCode(err, core.CodeNotFound) || (err == nil && record.State != core.StateDeveloping) {
			return nil
		}
		if err != nil {
			held = true
			return err
		}
		outage, _, err := handoff.ReadBuildOutage(ctx, tx, record.RunID, record.Attempt)
		if core.HasCode(err, core.CodeNotFound) {
			return nil
		}
		if err != nil {
			held = true
			return err
		}
		if !outage.Held || handoff.HeldEndsRun(outage.Reason) {
			// A build held over a superseded policy, or over a base that
			// lacks its unit's release, is never resumed: its issue is
			// blocked instead.
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

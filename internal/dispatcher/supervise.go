package dispatcher

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// supervise moves every run this instance holds one step on, in a fixed order.
func (d *Dispatcher) supervise(ctx context.Context, res *TickResult) {
	runs := make([]*activeRun, 0, len(d.runs))
	for _, r := range d.runs {
		runs = append(runs, r)
	}
	slices.SortFunc(runs, func(a, b *activeRun) int { return cmp.Compare(a.run, b.run) })
	for _, r := range runs {
		if err := d.step(ctx, r, res); err != nil {
			if fencedOff(err) {
				delete(d.runs, r.run)
				res.Lost = append(res.Lost, r.issue)
				continue
			}
			res.Errors = append(res.Errors, fmt.Errorf("run %s of %s: %w", r.run, r.issue, err))
		}
	}
}

// fencedOff reports whether err says this instance no longer holds the run.
func fencedOff(err error) bool {
	return core.HasCode(err, core.CodeStaleFencingToken) || core.HasCode(err, core.CodeExpired)
}

// step looks at one run and the agent working on it, and acts: it starts an
// attempt that has not started, collects the artifacts of one that
// succeeded, and counts one that failed, crashed or ran out of time as a
// failed attempt.
func (d *Dispatcher) step(ctx context.Context, r *activeRun, res *TickResult) error {
	var (
		record  core.StateRecord
		run     RunRecord
		version int64
	)
	err := d.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		if record, err = tx.GetRecord(ctx, r.issue); err != nil {
			return err
		}
		run, version, err = readRunRecord(ctx, tx, r.run)
		return err
	})
	if err != nil {
		return err
	}
	if record.RunID != r.run || run.Status != RunWorking ||
		(record.State != core.StateClaimed && record.State != core.StateDeveloping) {
		// The run moved on or stopped; it holds no agent of ours any more.
		delete(d.runs, r.run)
		return nil
	}
	if run.Agent == nil {
		return d.startAttempt(ctx, r, run, version, res)
	}
	status, err := d.cfg.Agent.Status(ctx, *run.Agent)
	switch {
	case core.HasCode(err, core.CodeUnavailable) || core.HasCode(err, core.CodeNotFound):
		return d.failAttempt(ctx, r, run, version, core.CodeOf(err), "the agent run was lost: "+err.Error(), res)
	case err != nil:
		return err
	case status.Phase == agent.PhaseSucceeded:
		return d.succeed(ctx, r, run, version, res)
	case status.Phase == agent.PhaseFailed || status.Phase == agent.PhaseCancelled:
		return d.failAttempt(ctx, r, run, version, core.CodeInternal, "the agent run ended "+string(status.Phase)+": "+status.Detail, res)
	}
	if d.cfg.Clock.Now().Sub(run.AttemptStartedAt) < d.cfg.Limits.RunTimeout {
		// Still working within its time: renew the lease, which also proves
		// this instance still holds the run.
		lease, _, err := d.fenced(ctx, r.issue, r.run, r.lease, version, func(context.Context, state.Tx, core.StateRecord, *RunRecord) error { return nil })
		if err == nil {
			r.lease = lease
		}
		return err
	}
	// The cancel is best effort: the attempt is over either way, and a run
	// that ignores it is no longer watched or paid for by this attempt.
	cancelErr := d.cfg.Agent.Cancel(ctx, *run.Agent)
	detail := fmt.Sprintf("the attempt ran past the run timeout of %s", d.cfg.Limits.RunTimeout)
	if cancelErr != nil {
		detail += "; cancelling it failed: " + cancelErr.Error()
	}
	return d.failAttempt(ctx, r, run, version, core.CodeTimeout, detail, res)
}

// startAttempt starts the next agent attempt of a run, or stops the run when
// it has none left. The backend gets an idempotency key per attempt, so a
// start repeated after a crash returns the run started the first time. The
// first attempt moves the issue from Claimed to Developing in the same
// write that records the agent run.
func (d *Dispatcher) startAttempt(ctx context.Context, r *activeRun, run RunRecord, version int64, res *TickResult) error {
	if run.Attempt >= d.cfg.Limits.MaxAttempts {
		return d.exhaust(ctx, r, version, res)
	}
	attempt := run.Attempt + 1
	task := agent.Task{
		RunID: run.RunID, IssueID: run.Issue, Role: agent.RoleCoder, Step: core.StepCoding,
		Handoff: run.Brief.Handoff, Policy: run.Brief.Policy,
		Limits:         agent.Limits{Timeout: d.cfg.Limits.RunTimeout},
		IdempotencyKey: fmt.Sprintf("%s/attempt/%d", run.RunID, attempt),
	}
	// The workspace starts at the commit the run's policy is pinned to,
	// the commit the supervisor applies the patch to. A real agent's
	// workspace is checked out at it and cannot be made without it.
	ws := agent.WorkspaceRef{ID: string(run.RunID), BaseSHA: run.Brief.Policy.PolicySHA}
	ref, startErr := d.cfg.Agent.Start(ctx, task, ws, nil)
	now := d.cfg.Clock.Now()
	var exhausted bool
	failure := AttemptFailure{Attempt: attempt, At: now}
	if startErr != nil {
		failure.Code, failure.Detail = core.CodeOf(startErr), "the agent did not start: "+startErr.Error()
	}
	lease, _, err := d.fenced(ctx, r.issue, r.run, r.lease, version, func(ctx context.Context, tx state.Tx, record core.StateRecord, stored *RunRecord) error {
		stored.Attempt = attempt
		if startErr != nil {
			stored.Failures = append(stored.Failures, failure)
			if exhausted = attempt >= d.cfg.Limits.MaxAttempts; exhausted {
				return d.stopRun(ctx, tx, stored, r.lease)
			}
			return nil
		}
		stored.Agent, stored.AttemptStartedAt = &ref, now
		if record.State != core.StateClaimed {
			return nil
		}
		_, err := tx.Transition(ctx, core.Transition{
			IssueID: record.IssueID, From: core.StateClaimed, To: core.StateDeveloping, Event: core.EventDevelopmentStarted,
			ExpectedVersion: record.Version, IdempotencyKey: "develop/" + string(run.RunID),
			LeaseOwner: r.lease.Owner, FencingToken: r.lease.Token, RunID: run.RunID, Attempt: record.Attempt,
			ActorID: d.cfg.Owner, Reason: "the agent started working",
		})
		return err
	})
	if err != nil {
		return err
	}
	r.lease = lease
	if startErr != nil {
		res.Failed = append(res.Failed, FailedAttempt{Issue: r.issue, RunID: r.run, AttemptFailure: failure})
	}
	if exhausted {
		delete(d.runs, r.run)
		res.Exhausted = append(res.Exhausted, r.issue)
	}
	return nil
}

// failAttempt records a failed attempt and starts the next one, if any is left.
func (d *Dispatcher) failAttempt(ctx context.Context, r *activeRun, run RunRecord, version int64, code core.ErrorCode, detail string, res *TickResult) error {
	failure := AttemptFailure{Attempt: run.Attempt, Code: code, Detail: detail, At: d.cfg.Clock.Now()}
	lease, written, err := d.fenced(ctx, r.issue, r.run, r.lease, version, func(_ context.Context, _ state.Tx, _ core.StateRecord, stored *RunRecord) error {
		stored.Failures = append(stored.Failures, failure)
		stored.Agent, stored.AttemptStartedAt = nil, failure.At
		return nil
	})
	if err != nil {
		return err
	}
	r.lease = lease
	res.Failed = append(res.Failed, FailedAttempt{Issue: r.issue, RunID: r.run, AttemptFailure: failure})
	run.Agent = nil
	return d.startAttempt(ctx, r, run, written, res)
}

// succeed records the artifacts of the attempt that succeeded and lets the
// issue lease go: no agent works on the run any more.
func (d *Dispatcher) succeed(ctx context.Context, r *activeRun, run RunRecord, version int64, res *TickResult) error {
	artifacts, err := d.cfg.Agent.CollectArtifacts(ctx, *run.Agent)
	if err != nil {
		return err
	}
	_, _, err = d.fenced(ctx, r.issue, r.run, r.lease, version, func(ctx context.Context, tx state.Tx, _ core.StateRecord, stored *RunRecord) error {
		stored.Artifacts = mergeArtifacts(stored.Artifacts, artifacts)
		stored.Status = RunSucceeded
		return releaseIssueLease(ctx, tx, r.lease)
	})
	if err != nil {
		return err
	}
	delete(d.runs, r.run)
	res.Succeeded = append(res.Succeeded, r.issue)
	return nil
}

// exhaust stops a run that used every attempt.
func (d *Dispatcher) exhaust(ctx context.Context, r *activeRun, version int64, res *TickResult) error {
	_, _, err := d.fenced(ctx, r.issue, r.run, r.lease, version, func(ctx context.Context, tx state.Tx, _ core.StateRecord, stored *RunRecord) error {
		return d.stopRun(ctx, tx, stored, r.lease)
	})
	if err != nil {
		return err
	}
	delete(d.runs, r.run)
	res.Exhausted = append(res.Exhausted, r.issue)
	return nil
}

// stopRun marks a run exhausted, releases its reservation, since it will
// spend nothing more, and gives up its issue lease.
func (d *Dispatcher) stopRun(ctx context.Context, tx state.Tx, run *RunRecord, lease state.Lease) error {
	run.Status, run.Agent = RunExhausted, nil
	if err := d.cfg.Budget.Release(ctx, tx, run.ReservationID); err != nil {
		return err
	}
	return releaseIssueLease(ctx, tx, lease)
}

// releaseIssueLease gives up the issue lease. fenced renewed it in the same
// transaction, keeping its token, so the lease passed in is still the one held.
func releaseIssueLease(ctx context.Context, tx state.Tx, lease state.Lease) error {
	return tx.ReleaseLease(ctx, lease)
}

package wiring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Who blocks an issue whose run stopped, in the journal and on the lease,
// and how long it holds the issue lease for the one write.
const (
	stoppedRunActor    = "service:control-lifecycle"
	stoppedRunLeaseTTL = time.Minute
)

// blockStoppedRuns blocks every issue whose run stopped without a
// candidate, so it does not look worked on for ever: the lifecycle has no
// other step out of Claimed or Developing than a candidate.
//
// A run stopped when the dispatcher marked it exhausted: it used every
// coding attempt it was allowed. The dispatcher's record is the agent
// zone's; all it can do here is block its own issue, which starts no work
// and spends nothing, and only its attempt count and the code of its last
// failure are kept, never its text.
//
// A run also stopped when the supervisor held its build because the policy
// in force is no longer the one the run was coded under: such a patch is
// never built, and waiting changes nothing. The hold is the supervisor's
// own record, and it ends with the block.
//
// The step is written under the issue lease, which the dispatcher gave up
// when it stopped the run, with the record of why (handoff.RunStop) in the
// same transaction. An issue whose lease is still held is left for the
// next pass.
//
// The same transaction closes the run's budget reservation: a blocked run
// spends nothing more and its build never runs, so nothing else would, and
// a reservation left held counts against the budget cap for ever. The
// reservation closed is the one derived from the run named by the
// lifecycle record, with or without the dispatcher's record of the run,
// which the agent zone owns and may have rewritten or never written; a
// reservation that record names other than the run's own is left as it is
// and the block goes on.
func (c *Control) blockStoppedRuns(ctx context.Context) error {
	var held []core.StateRecord
	err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		held, err = tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateClaimed, core.StateDeveloping}})
		return err
	})
	if err != nil {
		return err
	}
	var failed []error
	for _, r := range held {
		stop, err := c.blockStoppedRun(ctx, r.IssueID)
		switch {
		case core.HasCode(err, core.CodeLeaseHeld):
		case err != nil:
			failed = append(failed, fmt.Errorf("block %s, whose run stopped: %w", r.IssueID, err))
		case stop != nil:
			c.log.Warn("blocked an issue whose run stopped without a candidate; it waits for the Owner's retry",
				"issue", stop.IssueID.String(), "run", string(stop.RunID), "cause", string(stop.Cause), "code", string(stop.LastCode))
			if stop.refused != nil {
				c.log.Error("the run record of a blocked issue names a reservation that is not its run's; it was left as it is",
					"issue", stop.IssueID.String(), "run", string(stop.RunID), "code", string(core.CodeOf(stop.refused)), "error", stop.refused.Error())
			}
		}
	}
	return errors.Join(failed...)
}

// blockedRun is the record of why a run's issue was blocked, and why its
// reservation was left as it is, if it was.
type blockedRun struct {
	handoff.RunStop
	refused error
}

// blockStoppedRun blocks issue when the run its record names stopped, and
// returns the record of why; nil when the run goes on.
func (c *Control) blockStoppedRun(ctx context.Context, issue core.IssueRef) (*blockedRun, error) {
	var written *blockedRun
	err := c.cfg.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		written = nil
		record, err := tx.GetRecord(ctx, issue)
		if err != nil {
			return err
		}
		stop, stopped, err := stoppedRun(ctx, tx, record, c.cfg.Clock.Now().UTC())
		if err != nil || !stopped {
			return err
		}
		run, err := runRecordOf(ctx, tx, record)
		if err != nil {
			return err
		}
		lease, err := tx.AcquireLease(ctx, state.IssueLeaseResource(issue), stoppedRunActor, stoppedRunLeaseTTL)
		if err != nil {
			return err
		}
		step := core.Transition{
			IssueID: issue, From: record.State, To: core.StateBlocked, Event: stopEvent(record.State),
			ExpectedVersion: record.Version, IdempotencyKey: "stop/" + string(record.RunID),
			LeaseOwner: lease.Owner, FencingToken: lease.Token, RunID: record.RunID, Attempt: record.Attempt,
			ActorID: stoppedRunActor, Reason: "the run stopped without a candidate: " + string(stop.Cause),
			EvidenceRefs: []string{handoff.BuildNamespace + ":" + handoff.RunStopKey(record.RunID)},
		}
		if err := statemachine.New(c.cfg.Clock, statemachine.Guards{}).Check(ctx, step); err != nil {
			return err
		}
		if _, err := tx.Transition(ctx, step); err != nil {
			return err
		}
		if err := handoff.PutRunStop(ctx, tx, stop); err != nil {
			return err
		}
		if stop.Cause == statemachine.BlockPolicySuperseded {
			if err := endHeldBuild(ctx, tx, record); err != nil {
				return err
			}
		}
		refused, err := c.closeStoppedReservation(ctx, tx, record.RunID, run)
		if err != nil {
			return err
		}
		if err := keepProgress(ctx, tx, issue); err != nil {
			return err
		}
		written = &blockedRun{RunStop: stop, refused: refused}
		return tx.ReleaseLease(ctx, lease)
	})
	return written, err
}

// stopEvent is the step that blocks an issue whose run stopped in from.
func stopEvent(from core.State) core.Event {
	if from == core.StateClaimed {
		return core.EventClaimStopped
	}
	return core.EventDevelopmentStopped
}

// stoppedRun reports whether the run record names stopped without a
// candidate, and the record of why.
func stoppedRun(ctx context.Context, tx state.Tx, record core.StateRecord, now time.Time) (handoff.RunStop, bool, error) {
	if record.RunID == "" || (record.State != core.StateClaimed && record.State != core.StateDeveloping) {
		return handoff.RunStop{}, false, nil
	}
	stop := handoff.RunStop{IssueID: record.IssueID, RunID: record.RunID, Attempt: record.Attempt, BlockedAt: now}
	if record.State == core.StateDeveloping {
		outage, _, err := handoff.ReadBuildOutage(ctx, tx, record.RunID, record.Attempt)
		switch {
		case err == nil && outage.Held && outage.Reason == handoff.HeldPolicySuperseded:
			stop.Cause = statemachine.BlockPolicySuperseded
			return stop, true, nil
		case err != nil && !core.HasCode(err, core.CodeNotFound):
			return stop, false, err
		}
	}
	run, err := runRecordOf(ctx, tx, record)
	if err != nil || run == nil {
		// No record of this run: nothing says the run stopped.
		return stop, false, err
	}
	if run.Status != dispatcher.RunExhausted {
		return stop, false, nil
	}
	stop.Cause, stop.Attempts = statemachine.BlockRunExhausted, run.Attempt
	if n := len(run.Failures); n > 0 && run.Failures[n-1].Code.Valid() {
		stop.LastCode = run.Failures[n-1].Code
	}
	return stop, true, nil
}

// runRecordOf reads the dispatcher's record of the run record names; nil
// when there is none or it is not a record of that run and issue.
func runRecordOf(ctx context.Context, tx state.Tx, record core.StateRecord) (*dispatcher.RunRecord, error) {
	entry, err := tx.GetKV(ctx, handoff.Namespace, "run/"+string(record.RunID))
	if core.HasCode(err, core.CodeNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var run dispatcher.RunRecord
	if json.Unmarshal(entry.Value, &run) != nil || run.RunID != record.RunID || run.Issue != record.IssueID {
		return nil, nil
	}
	return &run, nil
}

// closeStoppedReservation closes the reservation of runID, the run of the
// lifecycle record whose issue is being blocked. The reservation is the one
// the run's claim made, derived from that run's ID, never the one the
// dispatcher's record names: that record is the agent zone's, so it is not
// trusted, and it may be missing or name another run. Whether the run had
// usage without a reported cost, and its reported cost, are read from the
// dispatcher's record of runID, when there is one; without one the run
// counts as having neither. A record naming another reservation is returned as
// refused, and so is a missing reservation the record claims; the block
// still goes on. Any other failure fails the block, which the next pass
// retries.
func (c *Control) closeStoppedReservation(ctx context.Context, tx state.Tx, runID core.RunID, run *dispatcher.RunRecord) (refused error, err error) {
	own := budget.RunReservationID(runID)
	var spend dispatcher.RunSpend
	claimed := ""
	if run != nil && run.RunID == runID {
		spend, claimed = run.Spend, run.ReservationID
	}
	if claimed != "" && claimed != own {
		refused = core.NewError(core.CodeTampered, "the record of run "+string(runID)+" names reservation "+claimed+", not its own "+own)
	}
	err = dispatcher.CloseReservationOfRun(ctx, c.budget, tx, runID, own, spend)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		if refused == nil && claimed != "" {
			refused = err
		}
		return refused, nil
	case core.HasCode(err, core.CodeTampered):
		return err, nil
	}
	return refused, err
}

// endHeldBuild removes the hold of the build of record's attempt, and its
// entry in the listing of held builds: the issue is blocked now and no
// build of that run waits for the Owner any more.
func endHeldBuild(ctx context.Context, tx state.Tx, record core.StateRecord) error {
	_, version, err := handoff.ReadBuildOutage(ctx, tx, record.RunID, record.Attempt)
	if err != nil {
		return err
	}
	return handoff.DeleteBuildOutage(ctx, tx, record.RunID, record.Attempt, version)
}

// keepProgress makes sure issue has a progress entry, so its progress
// comment is written once more with the block and why, also when the run
// stopped before the comment was ever written.
func keepProgress(ctx context.Context, tx state.Tx, issue core.IssueRef) error {
	_, err := tx.GetKV(ctx, progressNamespace, issue.String())
	if !core.HasCode(err, core.CodeNotFound) {
		return err
	}
	raw, err := json.Marshal(progressEntry{Issue: issue})
	if err != nil {
		return err
	}
	_, err = tx.PutKV(ctx, state.KVEntry{Namespace: progressNamespace, Key: issue.String(), Value: raw})
	return err
}

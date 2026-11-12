package dispatcher

import (
	"context"
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// claimBatch bounds how many Ready issues one tick looks at.
const claimBatch = 32

// claim claims Ready issues, oldest first, until the concurrency cap or the
// budget gate holds the rest back.
func (d *Dispatcher) claim(ctx context.Context, res *TickResult) {
	var ready []core.StateRecord
	err := d.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		ready, err = tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateReady}, Limit: claimBatch})
		return err
	})
	if err != nil {
		res.Errors = append(res.Errors, err)
		return
	}
	for _, record := range ready {
		brief, err := d.cfg.Briefs.Brief(ctx, record.IssueID)
		if err == nil {
			err = brief.validate()
		}
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("brief of %s: %w", record.IssueID, err))
			continue
		}
		run, err := d.claimOne(ctx, record, brief)
		switch {
		case err == nil:
			d.runs[run.run] = run
			res.Claimed = append(res.Claimed, record.IssueID)
		case holdsEveryClaim(err):
			res.Held = err
			return
		case core.HasCode(err, core.CodeLeaseHeld) || core.HasCode(err, core.CodeConflict):
			// Another writer got to the issue first; it is no longer ours to claim.
		default:
			res.Errors = append(res.Errors, fmt.Errorf("claim %s: %w", record.IssueID, err))
		}
	}
}

// holdsEveryClaim reports whether a refusal applies to every Ready issue
// alike, so trying the next one is pointless this tick.
func holdsEveryClaim(err error) bool {
	switch core.CodeOf(err) {
	case core.CodeLimitExceeded, core.CodeBudgetUnconfirmed, core.CodeBudgetExceeded, core.CodeNotSupported, core.CodeStaleFencingToken:
		return true
	}
	return false
}

// claimOne claims one issue in a single transaction: it proves this
// instance is still the active dispatcher, checks the concurrency cap, takes
// the issue lease, has the budget gate reserve for a new run, puts the claim
// before the lifecycle machine and writes it with the run record. A refusal
// anywhere writes nothing, the lease and the reservation included.
func (d *Dispatcher) claimOne(ctx context.Context, ready core.StateRecord, brief Brief) (*activeRun, error) {
	runID := d.cfg.NewRunID()
	if err := runID.Validate(); err != nil {
		return nil, err
	}
	var run *activeRun
	err := d.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if err := tx.CheckFencing(ctx, LeaseResource, d.leader.Token); err != nil {
			return err
		}
		working, err := countWorking(ctx, tx)
		if err != nil {
			return err
		}
		if working >= d.cfg.Limits.MaxConcurrent {
			return core.NewError(core.CodeLimitExceeded,
				fmt.Sprintf("%d runs are working, the most the concurrency cap allows", working))
		}
		stored, err := tx.GetRecord(ctx, ready.IssueID)
		if err != nil {
			return err
		}
		if stored.State != core.StateReady || stored.Version != ready.Version {
			return core.NewError(core.CodeConflict, "the issue changed after it was found Ready")
		}
		lease, err := tx.AcquireLease(ctx, state.IssueLeaseResource(ready.IssueID), d.cfg.Owner, d.cfg.LeaseTTL)
		if err != nil {
			return err
		}
		reservation, err := d.cfg.Budget.Reserve(ctx, tx, core.ReservationRequest{
			FactoryID: stored.FactoryID, IssueID: stored.IssueID, RunID: runID,
			Estimate: d.cfg.Estimate, IdempotencyKey: "claim/" + string(runID),
		})
		if err != nil {
			return err
		}
		step := core.Transition{
			IssueID: stored.IssueID, From: core.StateReady, To: core.StateClaimed, Event: core.EventClaimed,
			ExpectedVersion: stored.Version, IdempotencyKey: "claim/" + string(runID),
			LeaseOwner: lease.Owner, FencingToken: lease.Token, RunID: runID, Attempt: 1,
			ActorID: d.cfg.Owner, Reason: "claimed by the active dispatcher",
		}
		proof := statemachine.ClaimProof{
			LeaseOwner: lease.Owner, FencingToken: lease.Token, LeaseExpiresAt: lease.ExpiresAt, Reservation: reservation,
		}
		machine := statemachine.New(d.cfg.Clock, statemachine.Guards{
			Claim: statemachine.ClaimGuardFunc(func(context.Context, core.Transition) (statemachine.ClaimProof, error) {
				return proof, nil
			}),
		})
		if err := machine.Check(ctx, step); err != nil {
			return err
		}
		if _, err := tx.Transition(ctx, step); err != nil {
			return err
		}
		now := d.cfg.Clock.Now()
		record := RunRecord{
			Issue: stored.IssueID, RunID: runID, ReservationID: reservation.ID, Brief: brief,
			Status: RunWorking, ClaimedAt: now, UpdatedAt: now,
		}
		if _, err := writeRunRecord(ctx, tx, record, 0); err != nil {
			return err
		}
		run = &activeRun{issue: stored.IssueID, run: runID, lease: lease}
		return nil
	})
	return run, err
}

// countWorking counts the runs that have, or are about to have, an agent
// working: held issues whose run record is still working. A held issue
// without a record of this dispatcher counts too, since nothing says its
// run has stopped.
func countWorking(ctx context.Context, tx state.Tx) (int, error) {
	held, err := tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateClaimed, core.StateDeveloping}})
	if err != nil {
		return 0, err
	}
	working := 0
	for _, record := range held {
		run, _, err := readRunRecord(ctx, tx, record.RunID)
		switch {
		case core.HasCode(err, core.CodeNotFound):
			working++
		case err != nil:
			return 0, err
		case run.Status == RunWorking:
			working++
		}
	}
	return working, nil
}

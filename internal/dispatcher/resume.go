package dispatcher

import (
	"context"
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// resume takes over the working runs of held issues that no live lease
// covers, such as the runs of a dispatcher that crashed or was restarted.
// The run goes on from its stored record: same run, same brief, same agent
// run if one is in flight. An issue whose lease is still held is left to
// its holder.
func (d *Dispatcher) resume(ctx context.Context, res *TickResult) {
	var orphans []core.StateRecord
	err := d.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		held, err := tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateClaimed, core.StateDeveloping}})
		if err != nil {
			return err
		}
		for _, record := range held {
			if _, mine := d.runs[record.RunID]; mine {
				continue
			}
			run, _, err := readRunRecord(ctx, tx, record.RunID)
			switch {
			case core.HasCode(err, core.CodeNotFound):
				// Not claimed by a dispatcher; there is nothing to resume from.
				continue
			case err != nil:
				return err
			case run.Status == RunWorking:
				orphans = append(orphans, record)
			}
		}
		return nil
	})
	if err != nil {
		res.Errors = append(res.Errors, err)
		return
	}
	for _, record := range orphans {
		var lease state.Lease
		err := d.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			if err := tx.CheckFencing(ctx, LeaseResource, d.leader.Token); err != nil {
				return err
			}
			current, err := tx.GetRecord(ctx, record.IssueID)
			if err != nil {
				return err
			}
			if current.RunID != record.RunID {
				return core.NewError(core.CodeConflict, "the issue moved on to another run")
			}
			lease, err = tx.AcquireLease(ctx, state.IssueLeaseResource(record.IssueID), d.cfg.Owner, d.cfg.LeaseTTL)
			return err
		})
		switch {
		case err == nil:
			d.runs[record.RunID] = &activeRun{issue: record.IssueID, run: record.RunID, lease: lease}
			res.Resumed = append(res.Resumed, record.IssueID)
		case core.HasCode(err, core.CodeLeaseHeld) || core.HasCode(err, core.CodeConflict):
		default:
			res.Errors = append(res.Errors, fmt.Errorf("resume %s: %w", record.IssueID, err))
		}
	}
}

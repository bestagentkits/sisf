package main

import (
	"context"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// heldBuilds lists the held candidate builds of issues still waiting in
// Developing at the held attempt, for the supervisor's status.
func heldBuilds(store state.Store) supervisor.HeldBuildsReader {
	return func(ctx context.Context) ([]supervisor.HeldBuild, error) {
		var out []supervisor.HeldBuild
		err := store.View(ctx, func(ctx context.Context, tx state.Tx) error {
			held, err := handoff.ListHeldBuilds(ctx, tx)
			if err != nil {
				return err
			}
			out = nil
			for _, o := range held {
				record, err := tx.GetRecord(ctx, o.IssueID)
				if core.HasCode(err, core.CodeNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				if record.State != core.StateDeveloping || record.RunID != o.RunID || record.Attempt != o.Attempt {
					continue
				}
				out = append(out, supervisor.HeldBuild{IssueID: o.IssueID, RunID: o.RunID, Attempt: o.Attempt,
					Failures: o.Failures, LastCode: o.LastCode, Since: o.UpdatedAt})
			}
			return nil
		})
		return out, err
	}
}

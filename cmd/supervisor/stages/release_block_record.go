package stages

import (
	"context"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/state"
)

// releaseBlocks records why the release pipeline blocked an issue as the
// record of a block every other cause writes (handoff.RunStop), so the
// Owner's retry, the issue's progress and runs.explain read it alike.
type releaseBlocks struct{}

// RecordBlock writes the record in the transaction that blocks the issue.
func (releaseBlocks) RecordBlock(ctx context.Context, kv state.KV, b release.Block) error {
	return handoff.PutRunStop(ctx, kv, handoff.RunStop{
		IssueID: b.IssueID, RunID: b.RunID, Attempt: b.Attempt, Cause: b.Cause,
		ReleaseCode: b.Code, Release: b.Release, RestoredTo: b.RestoredTo, BlockedAt: b.BlockedAt,
	})
}

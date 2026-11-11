package release

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// failingBlocks cannot record a block until it is allowed to.
type failingBlocks struct{ allowed atomic.Bool }

func (b *failingBlocks) RecordBlock(ctx context.Context, kv state.KV, block Block) error {
	if !b.allowed.Load() {
		return errors.New("the store refused the block")
	}
	return storeBlocks{}.RecordBlock(ctx, kv, block)
}

// A restore that judged the release unhealthy but could not block the
// issue leaves it RolledBack; the recovery that finishes it blocks it with
// the cause the restore judged, not as interrupted.
func TestARolledBackReleaseIsBlockedWithTheCauseItsRestoreJudged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	blocks := &failingBlocks{}
	w.cfg.Blocks = blocks
	w.backend.errorRate[core.ReleaseDigest(firstBuild)] = 0.5
	issue, run := issueRef(86), core.RunID("run-judged")
	w.seedReleaseCandidate(issue, run, firstBuild)
	p := w.pipeline()
	if out, err := p.Release(ctx, w.request(issue, run, firstBuild)); err == nil || out.State != core.StateRolledBack {
		t.Fatalf("Release = %+v, %v; want left RolledBack", out, err)
	}
	blocks.allowed.Store(true)
	out, err := p.Recover(ctx, issue, testUnit)
	if err != nil || out.Result != ResultBlocked || out.Cause != statemachine.BlockRolledBack {
		t.Fatalf("Recover = %+v, %v; want blocked as rolled back", out, err)
	}
	requireBlockedWith(t, w, issue, run, statemachine.BlockRolledBack)
	if report, _ := w.recoveryReport(run); report.Reason == interruptedReason {
		t.Fatalf("the recovery report says the release was interrupted: %+v", report)
	}
}

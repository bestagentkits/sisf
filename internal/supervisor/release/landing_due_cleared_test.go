package release

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// landingDue reports whether the run still has a landing recorded as due.
func (w *world) landingDue(run core.RunID) bool {
	w.t.Helper()
	found := false
	err := w.store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		_, err := ReadLandingDue(ctx, tx, run)
		if core.HasCode(err, core.CodeNotFound) {
			return nil
		}
		found = err == nil
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return found
}

// The record that a verified release's landing is due lives only until the
// issue closes on the decided landing: it is deleted in the same step, so
// the records do not grow with every release, whether the landing was
// decided at once or finished later.
func TestTheLandingDueRecordIsDeletedWhenTheIssueClosesOnIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("decided at once", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.cfg.Lander = &countingLander{}
		issue, run := issueRef(31), core.RunID("run-31")
		w.seedReleaseCandidate(issue, run, firstBuild)
		if out, err := w.pipeline().Release(ctx, w.request(issue, run, firstBuild)); err != nil || out.Result != ResultClosed {
			t.Fatalf("Release = %+v, %v", out, err)
		}
		w.requireState(issue, core.StateClosed)
		if _, found := w.finishDue(run); found || w.landingDue(run) {
			t.Fatal("the landing is still recorded as due after the issue closed on it")
		}
	})
	t.Run("finished after an emergency stop", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.cfg.Lander = &countingLander{}
		w.cfg.Publisher = &freezingPublisher{freeze: w.freeze}
		issue, run := issueRef(32), core.RunID("run-32")
		w.seedReleaseCandidate(issue, run, firstBuild)
		req := w.request(issue, run, firstBuild)
		if _, err := w.pipeline().Release(ctx, req); !core.HasCode(err, core.CodeFrozen) {
			t.Fatalf("Release = %v; want held by the emergency stop", err)
		}
		if _, found := w.finishDue(run); !found {
			t.Fatal("a held landing is not recorded as due")
		}
		w.freeze.frozen.Store(false)
		if out, err := w.pipeline().FinishVerified(ctx, req); err != nil || out.Result != ResultClosed {
			t.Fatalf("FinishVerified = %+v, %v", out, err)
		}
		if _, found := w.finishDue(run); found || w.landingDue(run) {
			t.Fatal("the landing is still recorded as due after the issue closed on it")
		}
	})
}

package release

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Releases of a unit are ordered by the order their Verified steps were
// written in the store, not by the clock: a release verified after the
// clock was set back keeps the LKG over the one verified before, whichever
// is finished first.
func TestAReleaseVerifiedAfterTheClockWasSetBackIsStillTheLaterOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Lander = &countingLander{}
	unlock := w.lockTrustVolume()
	w.clock.Advance(3 * time.Hour)

	first, firstRun := issueRef(240), core.RunID("run-240")
	w.seedReleaseCandidate(first, firstRun, firstBuild)
	firstReq := w.request(first, firstRun, firstBuild)
	if out, err := w.pipeline().Release(ctx, firstReq); err == nil || out.State != core.StateVerified {
		t.Fatalf("first Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	w.serve(lkgDigest)
	// The clock of the host is set back, by an hour more than the first
	// release took.
	w.clock.Advance(-2 * time.Hour)

	second, secondRun := issueRef(241), core.RunID("run-241")
	w.seedReleaseCandidate(second, secondRun, secondBuild)
	secondReq := w.request(second, secondRun, secondBuild)
	if out, err := w.pipeline().Release(ctx, secondReq); err == nil || out.State != core.StateVerified {
		t.Fatalf("second Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	if w.record(second).UpdatedAt.After(w.record(first).UpdatedAt) {
		t.Fatal("the clock was not set back between the two Verified steps")
	}
	unlock()

	p := w.pipeline()
	if out, err := p.FinishVerified(ctx, secondReq); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified of the second = %+v, %v; want it closed", out, err)
	}
	if out, err := p.FinishVerified(ctx, firstReq); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified of the first = %+v, %v; want it closed as superseded", out, err)
	}
	if lkg := w.lkg(); lkg.Digest != core.ReleaseDigest(secondBuild) || lkg.RunID != secondRun {
		t.Fatalf("the LKG is %+v, want the second release, verified later, which serves", lkg)
	}
	if n := w.published(); n != 1 {
		t.Fatalf("%d releases were published, want only the one that serves", n)
	}
}

// An install after a release's Verified step supersedes the release however
// the clock dated the install: the install that pinned the LKG is not the
// one in force when the release was verified.
func TestAnInstallAfterTheVerifiedStepSupersedesTheReleaseWhateverTheClockSays(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Lander = &countingLander{}
	unlock := w.lockTrustVolume()
	issue, run := issueRef(242), core.RunID("run-242")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	if out, err := w.pipeline().Release(ctx, req); err == nil || out.State != core.StateVerified {
		t.Fatalf("Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	unlock()
	w.clock.Advance(-time.Hour)
	w.book = w.installAgain(installedBuild)
	w.cfg.LKG = w.book
	w.serve(installedBuild)

	out, err := w.pipeline().FinishVerified(ctx, req)
	if err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified = %+v, %v; want it closed as superseded by the install", out, err)
	}
	if lkg := w.lkg(); lkg.Digest != installedBuild {
		t.Fatalf("the LKG is %+v, want the release the newer install pins", lkg)
	}
	if n := w.published(); n != 0 {
		t.Fatalf("%d releases were published, want none: the verified release does not serve", n)
	}
}

// The counter of Verified steps is a document of the control zone, which
// factoryd shares. Deleted, it does not number a release verified later
// before the LKG: the release, which serves, still becomes the LKG and is
// published, rather than being closed as superseded by an earlier one.
func TestADeletedCounterDoesNotOrderALaterReleaseBeforeTheLKG(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Lander = &countingLander{}
	for i, build := range []core.Digest{firstBuild, secondBuild} {
		issue, run := issueRef(243+i), core.RunID(fmt.Sprintf("run-%d", 243+i))
		w.seedReleaseCandidate(issue, run, build)
		if out, err := w.pipeline().Release(ctx, w.request(issue, run, build)); err != nil || out.Result != ResultClosed {
			t.Fatalf("Release of %s = %+v, %v; want closed", build, out, err)
		}
	}
	if lkg := w.lkg(); lkg.VerifiedSequence != 2 {
		t.Fatalf("the LKG is %+v, want the second Verified step", lkg)
	}
	if err := w.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, RecoveryNamespace, verifiedSequenceKey)
		if err != nil {
			return err
		}
		return tx.DeleteKV(ctx, RecoveryNamespace, verifiedSequenceKey, entry.Version)
	}); err != nil {
		t.Fatal(err)
	}

	unlock := w.lockTrustVolume()
	issue, run := issueRef(245), core.RunID("run-245")
	w.seedReleaseCandidate(issue, run, otherBuild)
	req := w.request(issue, run, otherBuild)
	if out, err := w.pipeline().Release(ctx, req); err == nil || out.State != core.StateVerified {
		t.Fatalf("Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	unlock()
	if out, err := w.pipeline().FinishVerified(ctx, req); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified = %+v, %v; want closed", out, err)
	}
	if lkg := w.lkg(); lkg.Digest != core.ReleaseDigest(otherBuild) || lkg.VerifiedSequence <= 2 {
		t.Fatalf("the LKG is %+v, want the release verified last, numbered after the one before", lkg)
	}
	if n := w.published(); n != 3 {
		t.Fatalf("%d releases were published, want all three", n)
	}
}

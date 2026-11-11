package release

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
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

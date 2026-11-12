package release

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// seedEarlierBuildDue records what the verified release of run still owes
// as an earlier build wrote it, before Verified steps were numbered.
func (w *world) seedEarlierBuildDue(issue core.IssueRef, run core.RunID, build core.Digest) {
	w.t.Helper()
	err := w.store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		return putFinishDue(ctx, tx, FinishDue{IssueID: issue, RunID: run, Release: core.ReleaseDigest(build), Unit: testUnit})
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

// An LKG entry and a verified release that an earlier build both wrote
// before Verified steps were numbered are ordered by when their Verified
// steps were written, as that build ordered them: the release verified
// before the LKG's is superseded by it and closed unpublished, rather than
// blocked as no longer serving.
func TestAnEarlierBuildsLKGOrdersAnEarlierBuildsReleaseByItsVerifiedStep(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Lander = &countingLander{}
	issue, run := issueRef(246), core.RunID("run-246")
	w.seedVerified(issue, run, firstBuild)
	w.seedEarlierBuildDue(issue, run, firstBuild)
	w.clock.Advance(time.Minute)
	entry, err := json.Marshal(LKG{Unit: testUnit, Digest: core.ReleaseDigest(secondBuild), Source: SourcePromoted,
		IssueID: issueRef(247), RunID: "run-247", VerifiedAt: w.clock.Now().UTC(), VerifiedStepAt: w.clock.Now().UTC(), RecordedAt: w.clock.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.trustDir, "lkg-unit-"+testUnit+".json"), entry, 0o600); err != nil {
		t.Fatal(err)
	}
	w.serve(core.ReleaseDigest(secondBuild))

	out, err := w.pipeline().FinishVerified(ctx, w.request(issue, run, firstBuild))
	if err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified = %+v, %v; want closed as superseded by the LKG verified later", out, err)
	}
	if n := w.published(); n != 0 {
		t.Fatalf("%d releases were published, want none: the release does not serve", n)
	}
}

// Two verified releases an earlier build left, neither numbered, are
// ordered by when their Verified steps were written: the one verified first
// is superseded by the later one, which serves, rather than blocked.
func TestTwoReleasesOfAnEarlierBuildAreOrderedByTheirVerifiedSteps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Lander = &countingLander{}
	first, firstRun := issueRef(248), core.RunID("run-248")
	w.seedVerified(first, firstRun, firstBuild)
	w.seedEarlierBuildDue(first, firstRun, firstBuild)
	second, secondRun := issueRef(249), core.RunID("run-249")
	w.seedVerified(second, secondRun, secondBuild)
	w.seedEarlierBuildDue(second, secondRun, secondBuild)
	w.serve(core.ReleaseDigest(secondBuild))

	out, err := w.pipeline().FinishVerified(ctx, w.request(first, firstRun, firstBuild))
	if err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified of the first = %+v, %v; want closed as superseded by the second", out, err)
	}
	if n := w.published(); n != 0 {
		t.Fatalf("%d releases were published, want none yet", n)
	}
}

package release

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state"
)

// installedBuild is a release a later install of the factory pins.
const installedBuild core.ReleaseDigest = "sha256:6666666666666666666666666666666666666666666666666666666666666666"

// lockTrustVolume makes every write of the LKG fail until the returned
// function unlocks it, while what the volume holds can still be read.
func (w *world) lockTrustVolume() (unlock func()) {
	w.t.Helper()
	if err := os.Chmod(w.trustDir, 0o500); err != nil {
		w.t.Fatal(err)
	}
	unlocked := false
	unlock = func() {
		if !unlocked {
			unlocked = true
			if err := os.Chmod(w.trustDir, 0o700); err != nil {
				w.t.Fatal(err)
			}
		}
	}
	w.t.Cleanup(unlock)
	return unlock
}

// serve makes the running system serve digest, healthy, as a supervisor
// that started again and adopted it would leave it.
func (w *world) serve(digest core.ReleaseDigest) {
	w.backend.mu.Lock()
	defer w.backend.mu.Unlock()
	w.backend.existing[digest] = true
	w.backend.stable, w.backend.canary, w.backend.percent, w.backend.runningInstead = digest, "", 0, ""
}

// serving is the release the running system serves.
func (w *world) serving() deploy.Observed {
	w.t.Helper()
	observed, err := w.backend.ObservedRevision(context.Background(), testUnit)
	if err != nil {
		w.t.Fatal(err)
	}
	return observed
}

// published is how many releases were published.
func (w *world) published() int {
	w.publisher.mu.Lock()
	defer w.publisher.mu.Unlock()
	return len(w.publisher.entries)
}

// A verified release whose LKG could not be written, then a later release
// verified on top of the LKG adopted at a restart whose LKG could not be
// written either: once the trust volume is repaired, finishing the earlier
// one first does not take the LKG from the release that serves. The LKG is
// the later release, which serves, and the earlier one is not published as
// if it served.
func TestALaterVerifiedReleaseKeepsTheLKGWhateverOrderTheyAreFinishedIn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	lander := &recordingLander{}
	w.cfg.Lander = lander
	unlock := w.lockTrustVolume()

	first, firstRun := issueRef(200), core.RunID("run-200")
	w.seedReleaseCandidate(first, firstRun, firstBuild)
	firstReq := w.request(first, firstRun, firstBuild)
	if out, err := w.pipeline().Release(ctx, firstReq); err == nil || out.State != core.StateVerified {
		t.Fatalf("first Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	// The supervisor starts again and adopts the LKG it reads: the release
	// before the first one.
	w.serve(lkgDigest)
	w.clock.Advance(time.Hour)

	second, secondRun := issueRef(201), core.RunID("run-201")
	w.seedReleaseCandidate(second, secondRun, secondBuild)
	secondReq := w.request(second, secondRun, secondBuild)
	if out, err := w.pipeline().Release(ctx, secondReq); err == nil || out.State != core.StateVerified {
		t.Fatalf("second Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	unlock()

	p := w.pipeline()
	if out, err := p.FinishVerified(ctx, firstReq); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified of the first = %+v, %v; want it closed", out, err)
	}
	// At no point does the LKG name a release that does not serve.
	if lkg, found, err := w.book.Get(ctx, testUnit); err != nil || !found || lkg.Digest == core.ReleaseDigest(firstBuild) {
		t.Fatalf("after the first was finished the LKG is %+v (%v), the release that no longer serves", lkg, err)
	}
	w.clock.Advance(time.Minute)
	if out, err := p.FinishVerified(ctx, secondReq); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified of the second = %+v, %v; want it closed", out, err)
	}
	if lkg := w.lkg(); lkg.Digest != core.ReleaseDigest(secondBuild) || lkg.RunID != secondRun {
		t.Fatalf("the LKG is %+v, want the second release, which serves", lkg)
	}
	if serving := w.serving(); serving.Digest != core.ReleaseDigest(secondBuild) {
		t.Fatalf("%s serves, want the second release", serving.Digest)
	}
	if n := w.published(); n != 1 {
		t.Fatalf("%d releases were published, want only the one that serves", n)
	}
	// The earlier release's pull request is handed over as superseded, so
	// the factory does not merge a change that does not serve.
	landed := lander.requests()
	if len(landed) != 2 || landed[0].RunID != firstRun || landed[0].Superseded == "" || landed[1].RunID != secondRun || landed[1].Superseded != "" {
		t.Fatalf("the landings were %+v, want the first superseded and the second not", landed)
	}
}

// recordingLander records what it was asked to land and merges nothing.
type recordingLander struct {
	mu   sync.Mutex
	seen []LandRequest
}

func (l *recordingLander) Land(_ context.Context, req LandRequest) (Landing, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, req)
	return Landing{Summary: "left for the Owner"}, nil
}

func (l *recordingLander) requests() []LandRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.seen)
}

// At startup a verified release whose LKG was not written is recorded only
// when the running system serves it: one it does not serve is left as it
// is, without a step, for FinishVerified to judge on a later pass.
func TestTheLKGOfAVerifiedReleaseIsRecordedAtStartupOnlyWhenItServes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, serves := range []bool{false, true} {
		w := newWorld(t)
		unlock := w.lockTrustVolume()
		issue, run := issueRef(212), core.RunID("run-212")
		w.seedReleaseCandidate(issue, run, firstBuild)
		req := w.request(issue, run, firstBuild)
		if out, err := w.pipeline().Release(ctx, req); err == nil || out.State != core.StateVerified {
			t.Fatalf("Release = %+v, %v; want held in Verified on the LKG", out, err)
		}
		unlock()
		if !serves {
			w.serve(lkgDigest)
		}
		before := w.record(issue)

		if err := w.pipeline().RecordVerifiedLKG(ctx, req); err != nil {
			t.Fatalf("RecordVerifiedLKG (serving %v) = %v", serves, err)
		}
		want := lkgDigest
		if serves {
			want = core.ReleaseDigest(firstBuild)
		}
		if lkg := w.lkg(); lkg.Digest != want {
			t.Fatalf("serving %v, the LKG is %s, want %s", serves, lkg.Digest, want)
		}
		if after := w.record(issue); after != before {
			t.Fatalf("serving %v, the record moved from %+v to %+v; startup writes no step", serves, before, after)
		}
	}
}

// A verified release whose LKG could be written only after its QA receipt
// and the keys that signed it expired is still finished: its evidence is
// judged as of its verification, not as of now.
func TestAVerifiedReleaseIsFinishedAfterItsEvidenceExpired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	lander := &countingLander{}
	w.cfg.Lander = lander
	unlock := w.lockTrustVolume()
	issue, run := issueRef(202), core.RunID("run-202")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	if out, err := w.pipeline().Release(ctx, req); err == nil || out.State != core.StateVerified {
		t.Fatalf("Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	unlock()

	// A week later the receipt and both delegated keys have expired; the
	// supervisor signs the build again with the key it holds now.
	w.clock.Advance(8 * 24 * time.Hour)
	w.keys.qa = w.keys.delegate(t, delegation.PurposeQAReceipt)
	w.keys.artifact = w.keys.delegate(t, delegation.PurposeDeployArtifact)
	later := req
	later.Artifact = w.keys.artifactFor(t, firstBuild)

	if out, err := w.pipeline().FinishVerified(ctx, later); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified a week later = %+v, %v; want closed", out, err)
	}
	if lkg := w.lkg(); lkg.Digest != core.ReleaseDigest(firstBuild) || lkg.RunID != run {
		t.Fatalf("the LKG is %+v, want the verified release", lkg)
	}
	requireClosedOnce(t, w, issue, run, lander)
}

// failNextUpdate is a store whose next Update fails once armed.
type failNextUpdate struct {
	state.Store
	armed atomic.Bool
}

func (s *failNextUpdate) Update(ctx context.Context, fn func(context.Context, state.Tx) error) error {
	if s.armed.CompareAndSwap(true, false) {
		return core.NewError(core.CodeUnavailable, "the store is out")
	}
	return s.Store.Update(ctx, fn)
}

// armingBackend arms the store's failure on the nth observation of the
// running system.
type armingBackend struct {
	*fakeBackend
	store *failNextUpdate
	n     int32
	calls atomic.Int32
}

func (b *armingBackend) ObservedRevision(ctx context.Context, unit string) (deploy.Observed, error) {
	if b.calls.Add(1) == b.n {
		b.store.armed.Store(true)
	}
	return b.fakeBackend.ObservedRevision(ctx, unit)
}

// The Verified step that cannot be written after the observation window
// leaves the LKG as it was, so the release is restored to it and blocked
// rather than left in Monitoring with nothing to restore to once it turns
// unhealthy.
func TestAVerifiedStepThatCannotBeWrittenLeavesTheLKGToRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	store := &failNextUpdate{Store: w.store}
	w.cfg.State = store
	// The preflight, the release taking all traffic and the check after
	// the window observe the running system; the third arms the failure of
	// the next write, the Verified step's.
	w.cfg.Backend = &armingBackend{fakeBackend: w.backend, store: store, n: 3}
	issue, run := issueRef(203), core.RunID("run-203")
	w.seedReleaseCandidate(issue, run, firstBuild)
	_, _ = w.pipeline().Release(ctx, w.request(issue, run, firstBuild))

	w.backend.mu.Lock()
	w.backend.unhealthy[core.ReleaseDigest(firstBuild)] = true
	w.backend.mu.Unlock()
	_, _ = w.pipeline().Recover(ctx, issue, testUnit)

	w.requireState(issue, core.StateBlocked)
	if lkg := w.lkg(); lkg.Digest != lkgDigest {
		t.Fatalf("the LKG is %s, want the release before the one whose step failed", lkg.Digest)
	}
	if serving := w.serving(); serving.Digest != lkgDigest || !serving.Healthy {
		t.Fatalf("%+v serves, want the LKG, healthy", serving)
	}
}

// A release an earlier build of the supervisor recorded as the LKG before
// writing its Verified step, which then turned unhealthy in Monitoring, has
// nothing to be restored to: it is blocked as not judged, saying so, rather
// than waiting for a restore that cannot happen.
func TestAReleaseRecordedAsTheLKGThatTurnsUnhealthyIsBlockedNotLeftWaiting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	issue, run := issueRef(204), core.RunID("run-204")
	w.seedInFlight(issue, run, firstBuild, core.StateMonitoring)
	req := w.request(issue, run, firstBuild)
	if err := w.book.record(verifiedRelease{issue: issue, run: run, artifact: req.Artifact, receipt: req.Receipt, verifiedAt: w.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	w.backend.mu.Lock()
	w.backend.unhealthy[core.ReleaseDigest(firstBuild)] = true
	w.backend.mu.Unlock()

	out, err := w.pipeline().Recover(ctx, issue, testUnit)
	if err != nil || out.Result != ResultBlocked || out.Cause != statemachine.BlockReleaseUnjudged {
		t.Fatalf("Recover = %+v, %v; want blocked as not judged", out, err)
	}
	w.requireState(issue, core.StateBlocked)
	if report, found := w.recoveryReport(run); !found || report.RestoredTo != "" || report.Cause != statemachine.BlockReleaseUnjudged {
		t.Fatalf("recovery report %+v (found %v), want nothing restored and the cause release_unjudged", report, found)
	}
}

// installAgain records a later root-signed release manifest that pins
// digest, recorded now, and opens the book on it, as a new install of the
// factory leaves the trust volume.
func (w *world) installAgain(digest core.ReleaseDigest) *LKGBook {
	w.t.Helper()
	ctx := context.Background()
	verifier, err := signing.NewVerifier(w.keys.rootPub)
	if err != nil {
		w.t.Fatal(err)
	}
	store, err := supervisor.OpenLKGStore(w.trustDir, filepath.Join(w.trustDir, supervisor.DefaultLKGFile), testFactory, verifier)
	if err != nil {
		w.t.Fatal(err)
	}
	now := w.clock.Now()
	signed, err := signing.SignManifest(ctx, w.keys.root, signing.ReleaseManifest{
		APIVersion: signing.ReleaseAPIVersion, FactoryID: testFactory, IssuedAt: now,
		Units:                map[manifest.UnitName]core.Digest{manifest.UnitName(testUnit): core.Digest(digest), manifest.UnitSupervisor: suiteDigest},
		SupervisorExecutable: suiteDigest,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if err := store.Save(ctx, supervisor.LKGRecord{Release: signed, RecordedAt: now}); err != nil {
		w.t.Fatal(err)
	}
	book, err := OpenLKGBook(w.trustDir, testFactory, store, w.clock)
	if err != nil {
		w.t.Fatal(err)
	}
	return book
}

// A release verified before the Owner installed the factory again, whose
// LKG was never written, does not overwrite the release the newer
// root-signed install pins and serves.
func TestAnOlderVerifiedReleaseDoesNotOverwriteANewerInstall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Lander = &countingLander{}
	unlock := w.lockTrustVolume()
	issue, run := issueRef(205), core.RunID("run-205")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	if out, err := w.pipeline().Release(ctx, req); err == nil || out.State != core.StateVerified {
		t.Fatalf("Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	unlock()
	w.clock.Advance(time.Hour)
	w.book = w.installAgain(installedBuild)
	w.cfg.LKG = w.book
	w.serve(installedBuild)

	_, _ = w.pipeline().FinishVerified(ctx, req)
	if lkg := w.lkg(); lkg.Digest != installedBuild {
		t.Fatalf("the LKG is %+v, want the release the newer install pins", lkg)
	}
	if n := w.published(); n != 0 {
		t.Fatalf("%d releases were published, want none: the verified release does not serve", n)
	}
}

// A verified release that no longer serves, and that no later release
// replaced, cannot be finished: it is blocked rather than recorded as the
// LKG of a unit it does not serve or left waiting in Verified.
func TestAVerifiedReleaseThatNoLongerServesIsBlocked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	lander := &countingLander{}
	w.cfg.Lander = lander
	unlock := w.lockTrustVolume()
	issue, run := issueRef(206), core.RunID("run-206")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	if out, err := w.pipeline().Release(ctx, req); err == nil || out.State != core.StateVerified {
		t.Fatalf("Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	unlock()
	w.serve(lkgDigest)

	_, _ = w.pipeline().FinishVerified(ctx, req)
	w.requireState(issue, core.StateBlocked)
	if lkg := w.lkg(); lkg.Digest != lkgDigest {
		t.Fatalf("the LKG is %s, want the release that serves", lkg.Digest)
	}
	if lander.lands.Load() != 0 || w.published() != 0 {
		t.Fatal("a release that does not serve was merged or published")
	}
}

// A verified release that serves but is no longer healthy is restored to
// the LKG before its issue is blocked, so no unjudged program keeps
// serving.
func TestAnUnhealthyVerifiedReleaseIsRestoredToTheLKGThenBlocked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	lander := &countingLander{}
	w.cfg.Lander = lander
	unlock := w.lockTrustVolume()
	issue, run := issueRef(207), core.RunID("run-207")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	if out, err := w.pipeline().Release(ctx, req); err == nil || out.State != core.StateVerified {
		t.Fatalf("Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	unlock()
	w.backend.mu.Lock()
	w.backend.unhealthy[core.ReleaseDigest(firstBuild)] = true
	w.backend.mu.Unlock()

	_, _ = w.pipeline().FinishVerified(ctx, req)
	w.requireState(issue, core.StateBlocked)
	if serving := w.serving(); serving.Digest != lkgDigest || !serving.Healthy {
		t.Fatalf("%+v serves, want the LKG restored", serving)
	}
	if report, found := w.recoveryReport(run); !found || report.RestoredTo != lkgDigest || report.Cause != statemachine.BlockReleaseUnjudged {
		t.Fatalf("recovery report %+v (found %v), want the LKG restored, cause release_unjudged", report, found)
	}
	if lander.lands.Load() != 0 || w.published() != 0 {
		t.Fatal("an unjudged release was merged or published")
	}
}

// What a verified release owes is a document the control zone can write.
// One written for a run whose record is not Verified finishes nothing:
// nothing is published, merged or recorded as the LKG, and the record
// does not move.
func TestAForgedFinishDueForARunThatIsNotVerifiedDoesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	lander := &countingLander{}
	w.cfg.Lander = lander
	for i, left := range []core.State{core.StateReleaseCandidate, core.StateMonitoring} {
		issue, run := issueRef(208+i), core.RunID(fmt.Sprintf("run-forged-%d", i))
		if left == core.StateReleaseCandidate {
			w.seedReleaseCandidate(issue, run, firstBuild)
		} else {
			w.seedInFlight(issue, run, firstBuild, left)
			w.clock.Advance(3 * DefaultWindow)
		}
		before := w.record(issue)
		if err := w.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			return putFinishDue(ctx, tx, FinishDue{IssueID: issue, RunID: run, Release: core.ReleaseDigest(firstBuild), Unit: testUnit, Published: true})
		}); err != nil {
			t.Fatal(err)
		}
		if out, err := w.pipeline().FinishVerified(ctx, w.request(issue, run, firstBuild)); err == nil || out.Result == ResultClosed {
			t.Fatalf("%s: FinishVerified = %+v, %v; want nothing finished", left, out, err)
		}
		if after := w.record(issue); after.State != before.State || after.Version != before.Version {
			t.Fatalf("%s: the record moved from %+v to %+v", left, before, after)
		}
	}
	if lander.lands.Load() != 0 || w.published() != 0 {
		t.Fatal("a forged finish due merged or published a release")
	}
	if lkg := w.lkg(); lkg.Digest != lkgDigest {
		t.Fatalf("the LKG moved to %s", lkg.Digest)
	}
}

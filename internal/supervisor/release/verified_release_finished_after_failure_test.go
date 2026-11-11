package release

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// finishDue reads what the verified release of run still owes.
func (w *world) finishDue(run core.RunID) (FinishDue, bool) {
	w.t.Helper()
	var due FinishDue
	found := false
	err := w.store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		due, err = ReadFinishDue(ctx, tx, run)
		if core.HasCode(err, core.CodeNotFound) {
			return nil
		}
		found = err == nil
		return err
	})
	if err != nil {
		w.t.Fatalf("read the finish due of %s: %v", run, err)
	}
	return due, found
}

// forgetFinishDue deletes the finish-due entry of run, as a record left
// Verified by an earlier build of the supervisor has none.
func (w *world) forgetFinishDue(run core.RunID) {
	w.t.Helper()
	err := w.store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, RecoveryNamespace, FinishDueKey(run))
		if err != nil {
			return err
		}
		return tx.DeleteKV(ctx, RecoveryNamespace, FinishDueKey(run), entry.Version)
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

// requireClosedOnce fails the test unless issue is Closed, its change was
// merged once and its release published once, and nothing is due any more.
func requireClosedOnce(t *testing.T, w *world, issue core.IssueRef, run core.RunID, lander *countingLander) {
	t.Helper()
	w.requireState(issue, core.StateClosed)
	if n := lander.lands.Load(); n != 1 {
		t.Fatalf("the change was merged %d times, want once", n)
	}
	w.publisher.mu.Lock()
	published := len(w.publisher.entries)
	w.publisher.mu.Unlock()
	if published != 1 {
		t.Fatalf("the release was published %d times, want once", published)
	}
	if _, due := w.finishDue(run); due || w.landingDue(run) {
		t.Fatal("the finished release is still recorded as due")
	}
}

// A publication that fails after the release was verified leaves the issue
// Verified with its finish due and says what it waits on; a later pass
// publishes, merges and closes it, once.
func TestAVerifiedReleaseWhosePublicationFailedIsFinishedOnALaterPass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	lander := &countingLander{}
	w.cfg.Lander = lander
	w.publisher.err = core.NewError(core.CodeUnavailable, "GitHub is out")
	issue, run := issueRef(70), core.RunID("run-70")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)

	out, err := w.pipeline().Release(ctx, req)
	if !core.HasCode(err, core.CodeUnavailable) || out.Result != ResultHeld || out.State != core.StateVerified || out.Waits == "" {
		t.Fatalf("Release = %+v, %v; want held in Verified, saying what it waits on", out, err)
	}
	if due, found := w.finishDue(run); !found || due.Published {
		t.Fatalf("finish due = %+v (found %v), want due and not published", due, found)
	}
	if lkg := w.lkg(); lkg.Digest != core.ReleaseDigest(firstBuild) || lkg.RunID != run {
		t.Fatalf("the LKG is %+v, want the verified release", lkg)
	}
	if out, err := w.pipeline().FinishVerified(ctx, req); !core.HasCode(err, core.CodeUnavailable) || out.Result != ResultHeld {
		t.Fatalf("FinishVerified during the outage = %+v, %v; want held", out, err)
	}

	w.publisher.mu.Lock()
	w.publisher.err = nil
	w.publisher.mu.Unlock()
	if out, err := w.pipeline().FinishVerified(ctx, req); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified = %+v, %v; want closed", out, err)
	}
	if out, err := w.pipeline().FinishVerified(ctx, req); err == nil || out.Result == ResultClosed {
		t.Fatalf("a second FinishVerified = %+v, %v; want nothing finished again", out, err)
	}
	requireClosedOnce(t, w, issue, run, lander)
}

// When the LKG cannot be recorded, the verified release is not published
// or merged; a later pass records it, then finishes the release.
func TestALastKnownGoodThatCouldNotBeRecordedIsRecordedOnALaterPass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	lander := &countingLander{}
	w.cfg.Lander = lander
	issue, run := issueRef(71), core.RunID("run-71")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	// A trust volume that cannot be written makes every write of the LKG
	// fail, while what it holds can still be read.
	if err := os.Chmod(w.trustDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(w.trustDir, 0o700) })

	out, err := w.pipeline().Release(ctx, req)
	if err == nil || out.Result != ResultHeld || out.State != core.StateVerified || out.Waits == "" {
		t.Fatalf("Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	if lander.lands.Load() != 0 || len(w.publisher.entries) != 0 {
		t.Fatal("a release whose LKG was not recorded was published or merged")
	}
	if err := os.Chmod(w.trustDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := w.pipeline().FinishVerified(ctx, req); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified = %+v, %v; want closed", out, err)
	}
	if lkg := w.lkg(); lkg.Digest != core.ReleaseDigest(firstBuild) || lkg.RunID != run {
		t.Fatalf("the LKG is %+v, want the verified release", lkg)
	}
	requireClosedOnce(t, w, issue, run, lander)
}

// verifiedFreeze is an emergency stop that takes effect the moment the
// issue is verified, until it is lifted.
type verifiedFreeze struct {
	w      *world
	issue  core.IssueRef
	lifted bool
}

func (f *verifiedFreeze) Frozen(ctx context.Context) (bool, error) {
	if f.lifted {
		return false, nil
	}
	var record core.StateRecord
	err := f.w.store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		record, err = tx.GetRecord(ctx, f.issue)
		return err
	})
	return record.State == core.StateVerified, err
}

// An emergency stop at the freeze check after the verification holds the
// release in Verified; once the stop is lifted, it is finished.
func TestAVerifiedReleaseHeldByAnEmergencyStopIsFinishedOnceTheStopIsLifted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	lander := &countingLander{}
	w.cfg.Lander = lander
	issue, run := issueRef(72), core.RunID("run-72")
	freeze := &verifiedFreeze{w: w, issue: issue}
	w.cfg.Freeze = freeze
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)

	out, err := w.pipeline().Release(ctx, req)
	if !core.HasCode(err, core.CodeFrozen) || out.State != core.StateVerified || out.Waits == "" {
		t.Fatalf("Release = %+v, %v; want held in Verified by the emergency stop", out, err)
	}
	if out, err := w.pipeline().FinishVerified(ctx, req); !core.HasCode(err, core.CodeFrozen) || out.Waits == "" {
		t.Fatalf("FinishVerified during the stop = %+v, %v; want held by it", out, err)
	}
	freeze.lifted = true
	if out, err := w.pipeline().FinishVerified(ctx, req); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified after the stop = %+v, %v; want closed", out, err)
	}
	requireClosedOnce(t, w, issue, run, lander)
}

// A supervisor that stops between the verification and the landing leaves
// the verified release as the LKG, so the restarted supervisor serves it,
// and a new pipeline on the same store finishes the release.
func TestARestartBetweenTheVerificationAndTheLandingKeepsTheVerifiedRelease(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	lander := &countingLander{}
	w.cfg.Lander = lander
	issue, run := issueRef(73), core.RunID("run-73")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	stopping, stop := context.WithCancel(context.Background())
	w.publisher.err = errors.New("the supervisor is stopping")
	w.publisher.hook = stop

	if _, err := w.pipeline().Release(stopping, req); err == nil {
		t.Fatal("a release cut short by a stopping supervisor reported no error")
	}
	w.requireState(issue, core.StateVerified)

	// The restarted supervisor: a new book and a new pipeline on the same
	// trust volume and store. Adoption starts what the book names.
	w.book = w.recordInstallLKG(lkgDigest)
	w.cfg.LKG = w.book
	if lkg := w.lkg(); lkg.Digest != core.ReleaseDigest(firstBuild) || lkg.RunID != run {
		t.Fatalf("after the restart the LKG is %+v, want the verified release", lkg)
	}
	w.publisher.mu.Lock()
	w.publisher.err, w.publisher.hook = nil, nil
	w.publisher.mu.Unlock()
	if out, err := w.pipeline().FinishVerified(context.Background(), req); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified = %+v, %v; want closed", out, err)
	}
	requireClosedOnce(t, w, issue, run, lander)
}

// A supervisor that stopped after recording the LKG but before writing the
// Verified step left the release in Monitoring as the LKG: the recovery
// verifies it from the running system instead of restoring it to itself.
func TestAReleaseRecordedAsTheLKGBeforeItsVerificationIsVerifiedByRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	lander := &countingLander{}
	w.cfg.Lander = lander
	issue, run := issueRef(74), core.RunID("run-74")
	w.seedInFlight(issue, run, firstBuild, core.StateMonitoring)
	req := w.request(issue, run, firstBuild)
	if err := w.book.record(verifiedRelease{issue: issue, run: run, artifact: req.Artifact, receipt: req.Receipt, verifiedAt: w.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	p := w.pipeline()
	out, err := p.Recover(ctx, issue, testUnit)
	if err != nil || out.State != core.StateVerified {
		t.Fatalf("Recover = %+v, %v; want the release verified", out, err)
	}
	if _, rollbacks, _, _ := w.backend.snapshot(); len(rollbacks) != 0 {
		t.Fatalf("the recovery rolled back to %v", rollbacks)
	}
	if out, err := p.FinishVerified(ctx, req); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified = %+v, %v; want closed", out, err)
	}
	requireClosedOnce(t, w, issue, run, lander)
}

// A record an earlier build of the supervisor left Verified with nothing
// recorded as due, and whose LKG it may not have recorded, is finished:
// the LKG is recorded after the release is verified again, then it is
// published, merged and closed.
func TestAVerifiedRecordLeftByAnEarlierBuildIsFinished(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	lander := &countingLander{}
	w.cfg.Lander = lander
	w.publisher.err = core.NewError(core.CodeUnavailable, "GitHub is out")
	issue, run := issueRef(75), core.RunID("run-75")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	if _, err := w.pipeline().Release(ctx, req); err == nil {
		t.Fatal("want the release held on the publication")
	}
	w.forgetFinishDue(run)
	if err := os.Remove(filepath.Join(w.trustDir, "lkg-unit-"+testUnit+".json")); err != nil {
		t.Fatal(err)
	}
	w.publisher.mu.Lock()
	w.publisher.err = nil
	w.publisher.mu.Unlock()
	if out, err := w.pipeline().FinishVerified(ctx, req); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified = %+v, %v; want closed", out, err)
	}
	if lkg := w.lkg(); lkg.Digest != core.ReleaseDigest(firstBuild) || lkg.RunID != run {
		t.Fatalf("the LKG is %+v, want the verified release", lkg)
	}
	requireClosedOnce(t, w, issue, run, lander)
}

// A verified release a later release superseded as the LKG is finished
// without being made the LKG again.
func TestAFinishNeverTakesTheLKGBackFromALaterRelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Lander = &countingLander{}
	w.publisher.err = core.NewError(core.CodeUnavailable, "GitHub is out")
	issue, run := issueRef(76), core.RunID("run-76")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	if _, err := w.pipeline().Release(ctx, req); err == nil {
		t.Fatal("want the release held on the publication")
	}
	later := w.request(issueRef(77), "run-77", secondBuild)
	w.clock.Advance(DefaultWindow)
	if err := w.book.record(verifiedRelease{issue: later.IssueID, run: later.RunID, artifact: later.Artifact, receipt: later.Receipt, verifiedAt: w.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	w.forgetFinishDue(run)
	w.publisher.mu.Lock()
	w.publisher.err = nil
	w.publisher.mu.Unlock()
	if out, err := w.pipeline().FinishVerified(ctx, req); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified = %+v, %v; want closed", out, err)
	}
	if lkg := w.lkg(); lkg.Digest != core.ReleaseDigest(secondBuild) {
		t.Fatalf("the LKG went back to %s, want the later release %s", lkg.Digest, secondBuild)
	}
}

// A supervisor that starts again after its LKG could not be written records
// the LKG of the verified release before adopting it, so it serves what the
// record says rather than the release before it.
func TestTheLKGOfAVerifiedReleaseIsRecordedBeforeItIsAdoptedAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	issue, run := issueRef(78), core.RunID("run-78")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	if err := os.Chmod(w.trustDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(w.trustDir, 0o700) })
	if out, err := w.pipeline().Release(ctx, req); err == nil || out.State != core.StateVerified {
		t.Fatalf("Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	if err := os.Chmod(w.trustDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if lkg := w.lkg(); lkg.Digest != lkgDigest {
		t.Fatalf("the LKG is %s before the restart, want the one before the release", lkg.Digest)
	}
	if err := w.pipeline().RecordVerifiedLKG(ctx, req); err != nil {
		t.Fatal(err)
	}
	if lkg := w.lkg(); lkg.Digest != core.ReleaseDigest(firstBuild) || lkg.RunID != run {
		t.Fatalf("the LKG adopted at the restart is %+v, want the verified release", lkg)
	}
	w.requireState(issue, core.StateVerified)
}

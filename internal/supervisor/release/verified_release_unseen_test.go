package release

import (
	"context"
	"fmt"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
)

// finishAfterUnseenWait finishes req with p once, which must hold the
// release while it is not seen, then once more after the wait has run out.
func (w *world) finishAfterUnseenWait(p *Pipeline, req Request) (Outcome, error) {
	w.t.Helper()
	ctx := context.Background()
	out, err := p.FinishVerified(ctx, req)
	if !core.HasCode(err, core.CodeUnavailable) || out.Result != ResultHeld || out.State != core.StateVerified {
		w.t.Fatalf("FinishVerified while the release is not seen = %+v, %v; want held, UNAVAILABLE", out, err)
	}
	w.clock.Advance(p.unseenWait())
	return p.FinishVerified(ctx, req)
}

// verifiedOnLockedLKG releases build for a new issue while the LKG cannot
// be written, so the release is left Verified for FinishVerified.
func (w *world) verifiedOnLockedLKG(n int, run core.RunID) Request {
	w.t.Helper()
	unlock := w.lockTrustVolume()
	defer unlock()
	issue := issueRef(n)
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	if out, err := w.pipeline().Release(context.Background(), req); err == nil || out.State != core.StateVerified {
		w.t.Fatalf("Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	return req
}

// A health probe that fails for a moment, or the release answering its
// health check as down for a moment, judges nothing: the verified release
// is held, nothing is switched or blocked, and once it is seen serving,
// healthy, it is recorded as the LKG and finished.
func TestAFailedProbeDoesNotBlockAHealthyVerifiedRelease(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		mark func(b *fakeBackend, release core.ReleaseDigest)
	}{
		{"the probe finds nothing", func(b *fakeBackend, r core.ReleaseDigest) { b.unhealthy[r] = true }},
		{"the release answers as down", func(b *fakeBackend, r core.ReleaseDigest) { b.degraded[r] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			w := newWorld(t)
			lander := &countingLander{}
			w.cfg.Lander = lander
			req := w.verifiedOnLockedLKG(250, "run-250")
			release := core.ReleaseDigest(firstBuild)
			w.backend.mu.Lock()
			tc.mark(w.backend, release)
			w.backend.mu.Unlock()

			p := w.pipeline()
			out, err := p.FinishVerified(ctx, req)
			if !core.HasCode(err, core.CodeUnavailable) || out.Result != ResultHeld || out.Waits == "" {
				t.Fatalf("FinishVerified = %+v, %v; want held, UNAVAILABLE, saying what it waits on", out, err)
			}
			w.requireState(req.IssueID, core.StateVerified)
			if _, rollbacks, _, _ := w.backend.snapshot(); len(rollbacks) != 0 {
				t.Fatalf("the unit was switched to %v on a probe that judged nothing", rollbacks)
			}
			// The probe answers again within the wait.
			w.clock.Advance(p.unseenWait() / 2)
			w.backend.mu.Lock()
			w.backend.unhealthy, w.backend.degraded = map[core.ReleaseDigest]bool{}, map[core.ReleaseDigest]bool{}
			w.backend.mu.Unlock()

			if out, err := p.FinishVerified(ctx, req); err != nil || out.Result != ResultClosed {
				t.Fatalf("FinishVerified once it is seen = %+v, %v; want closed", out, err)
			}
			if lkg := w.lkg(); lkg.Digest != release || lkg.RunID != req.RunID {
				t.Fatalf("the LKG is %+v, want the verified release", lkg)
			}
			if lander.lands.Load() != 1 || w.published() != 1 {
				t.Fatal("the verified release was not merged and published once")
			}
		})
	}
}

// A release that is not seen is waited for only as long as a release is
// watched before it is verified, counted from the first pass that did not
// see it, then restored to the LKG and blocked; the block says why.
func TestAVerifiedReleaseNotSeenForTheObservationWindowsIsRestoredThenBlocked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Lander = &countingLander{}
	req := w.verifiedOnLockedLKG(251, "run-251")
	w.backend.mu.Lock()
	w.backend.degraded[core.ReleaseDigest(firstBuild)] = true
	w.backend.mu.Unlock()

	p := w.pipeline()
	if _, err := p.FinishVerified(ctx, req); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("first pass = %v, want UNAVAILABLE", err)
	}
	w.clock.Advance(p.unseenWait() - 1)
	if _, err := p.FinishVerified(ctx, req); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("a pass just before the wait ran out = %v, want UNAVAILABLE", err)
	}
	w.requireState(req.IssueID, core.StateVerified)
	w.clock.Advance(1)
	out, err := p.FinishVerified(ctx, req)
	if err != nil || out.Result != ResultBlocked || out.Cause != statemachine.BlockReleaseUnjudged || out.RestoredTo != lkgDigest {
		t.Fatalf("FinishVerified once the wait ran out = %+v, %v; want restored to the LKG and blocked", out, err)
	}
	if report, found := w.recoveryReport(req.RunID); !found || report.RestoredTo != lkgDigest || report.Reason == "" {
		t.Fatalf("recovery report %+v (found %v), want the LKG restored and the reason given", report, found)
	}
	if serving := w.serving(); serving.Digest != lkgDigest || !serving.Healthy {
		t.Fatalf("%+v serves, want the LKG", serving)
	}
}

// setDegraded makes the verified release answer its health check as down,
// or healthy again.
func (w *world) setDegraded(release core.ReleaseDigest, down bool) {
	w.backend.mu.Lock()
	defer w.backend.mu.Unlock()
	w.backend.degraded[release] = down
}

// A pass that sees the verified release serving, healthy, clears its wait
// even when a later step of that pass fails, here the write of the LKG: a
// release not seen again afterwards waits the whole wait from then, rather
// than being blocked for the time before it was seen.
func TestAHealthyObservationClearsTheWaitEvenWhenALaterStepFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Lander = &countingLander{}
	req := w.verifiedOnLockedLKG(252, "run-252")
	release := core.ReleaseDigest(firstBuild)
	p := w.pipeline()
	w.setDegraded(release, true)
	if _, err := p.FinishVerified(ctx, req); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("first pass = %v, want UNAVAILABLE", err)
	}
	w.clock.Advance(p.unseenWait() / 2)
	w.setDegraded(release, false)
	unlock := w.lockTrustVolume()
	if out, err := p.FinishVerified(ctx, req); err == nil || out.Result == ResultBlocked {
		t.Fatalf("a pass that sees the release but cannot record it = %+v, %v; want held", out, err)
	}
	unlock()
	w.setDegraded(release, true)
	w.clock.Advance(p.unseenWait() / 2)
	out, err := p.FinishVerified(ctx, req)
	if !core.HasCode(err, core.CodeUnavailable) || out.Result != ResultHeld {
		t.Fatalf("a pass a whole wait after the first but less after it was seen = %+v, %v; want held", out, err)
	}
	w.requireState(req.IssueID, core.StateVerified)
}

// Time under an emergency stop does not count toward the wait: a pass
// skipped for the stop starts the wait again, so a release is not blocked
// as soon as the stop is lifted for the time it was frozen.
func TestTheWaitOfAnUnseenReleaseDoesNotRunWhileFrozen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Lander = &countingLander{}
	req := w.verifiedOnLockedLKG(253, "run-253")
	p := w.pipeline()
	w.setDegraded(core.ReleaseDigest(firstBuild), true)
	if _, err := p.FinishVerified(ctx, req); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("first pass = %v, want UNAVAILABLE", err)
	}
	w.freeze.frozen.Store(true)
	w.clock.Advance(p.unseenWait())
	if _, err := p.FinishVerified(ctx, req); !core.HasCode(err, core.CodeFrozen) {
		t.Fatalf("a pass under the stop = %v, want FROZEN", err)
	}
	w.freeze.frozen.Store(false)
	out, err := p.FinishVerified(ctx, req)
	if !core.HasCode(err, core.CodeUnavailable) || out.Result != ResultHeld {
		t.Fatalf("the first pass after the stop = %+v, %v; want held, the wait started again", out, err)
	}
	w.requireState(req.IssueID, core.StateVerified)
}

// At startup every verified release that is not seen is waited for at
// once: the wait of each starts in the first pass over them all, so the
// supervisor waits one unseenWait before it adopts the LKG, however many
// there are, and each is left Verified for a later pass to judge.
func TestStartupWaitsForUnseenVerifiedReleasesTogether(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	var reqs []Request
	for i, build := range []core.Digest{firstBuild, secondBuild, otherBuild} {
		issue, run := issueRef(254+i), core.RunID(fmt.Sprintf("run-%d", 254+i))
		w.seedVerified(issue, run, build)
		reqs = append(reqs, w.request(issue, run, build))
	}
	// The probe finds nothing: no release is seen, nor another in its place.
	w.backend.mu.Lock()
	w.backend.unhealthy[w.backend.stable] = true
	w.backend.mu.Unlock()

	p := w.pipeline()
	before := w.clock.Now()
	for i, err := range p.RecordVerifiedLKGs(ctx, reqs) {
		if err != nil {
			t.Fatalf("RecordVerifiedLKGs of %s = %v", reqs[i].RunID, err)
		}
	}
	if waited := w.clock.Now().Sub(before); waited < p.unseenWait() || waited > p.unseenWait()+unseenPoll {
		t.Fatalf("startup waited %v for %d unseen releases, want one wait of %v", waited, len(reqs), p.unseenWait())
	}
	for _, req := range reqs {
		w.requireState(req.IssueID, core.StateVerified)
	}
}

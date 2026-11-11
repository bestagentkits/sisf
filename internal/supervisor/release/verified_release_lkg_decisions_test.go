package release

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/audit"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
)

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

// auditActions turns on the audit log of w and returns a function that
// lists the details of every entry with action, oldest first.
func (w *world) auditActions() func(action string) []map[string]string {
	log := audit.New(w.store, w.clock)
	w.cfg.Audit = log
	return func(action string) []map[string]string {
		w.t.Helper()
		events, err := log.Events(context.Background(), 0, 1000)
		if err != nil {
			w.t.Fatal(err)
		}
		var details []map[string]string
		for _, e := range events {
			if e.Action == action {
				details = append(details, e.Details)
			}
		}
		return details
	}
}

// A release a later one superseded is handed to the Lander marked as
// superseded, so the factory does not merge a change that does not serve,
// and it is closed without being published; the later release is landed
// as usual.
func TestASupersededReleaseIsLandedAsSupersededAndNotPublished(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	lander := &recordingLander{}
	w.cfg.Lander = lander
	unlock := w.lockTrustVolume()

	first, firstRun := issueRef(220), core.RunID("run-220")
	w.seedReleaseCandidate(first, firstRun, firstBuild)
	firstReq := w.request(first, firstRun, firstBuild)
	if out, err := w.pipeline().Release(ctx, firstReq); err == nil || out.State != core.StateVerified {
		t.Fatalf("first Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	w.serve(lkgDigest)
	w.clock.Advance(time.Hour)
	second, secondRun := issueRef(221), core.RunID("run-221")
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
	if n := w.published(); n != 0 {
		t.Fatalf("%d releases were published, want none for the superseded release", n)
	}
	if out, err := p.FinishVerified(ctx, secondReq); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified of the second = %+v, %v; want it closed", out, err)
	}
	landed := lander.requests()
	if len(landed) != 2 || landed[0].RunID != firstRun || landed[0].Superseded == "" || landed[1].RunID != secondRun || landed[1].Superseded != "" {
		t.Fatalf("the landings were %+v, want the first superseded and the second not", landed)
	}
	if n := w.published(); n != 1 {
		t.Fatalf("%d releases were published, want only the one that serves", n)
	}
}

// A running system that does not answer is an outage that clears: the
// verified release waits in Verified, neither recorded as the LKG nor
// blocked, and is finished once the system answers again.
func TestAVerifiedReleaseWaitsWhileTheRunningSystemCannotBeObserved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	lander := &countingLander{}
	w.cfg.Lander = lander
	unlock := w.lockTrustVolume()
	issue, run := issueRef(222), core.RunID("run-222")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	if out, err := w.pipeline().Release(ctx, req); err == nil || out.State != core.StateVerified {
		t.Fatalf("Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	unlock()
	w.backend.mu.Lock()
	w.backend.observeErr = core.NewError(core.CodeUnavailable, "the deploy backend is out")
	w.backend.mu.Unlock()

	p := w.pipeline()
	if out, err := p.FinishVerified(ctx, req); !core.HasCode(err, core.CodeUnavailable) || out.Waits == "" {
		t.Fatalf("FinishVerified while the system cannot be observed = %+v, %v; want a wait", out, err)
	}
	w.requireState(issue, core.StateVerified)
	if lkg := w.lkg(); lkg.Digest != lkgDigest {
		t.Fatalf("the LKG is %s while nothing could be observed, want the one before", lkg.Digest)
	}

	w.backend.mu.Lock()
	w.backend.observeErr = nil
	w.backend.mu.Unlock()
	if out, err := p.FinishVerified(ctx, req); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified once the system answers = %+v, %v; want closed", out, err)
	}
	requireClosedOnce(t, w, issue, run, lander)
}

// A rollback made without the issue lease is audited with the release it
// left serving, and the block a later recovery writes names that release
// as restored.
func TestARollbackWithoutTheLeaseIsAuditedAndItsBlockNamesWhatServes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	actions := w.auditActions()
	issue, run := issueRef(223), core.RunID("run-223")
	w.seedInFlight(issue, run, firstBuild, core.StateMonitoring)
	w.clock.Advance(3 * DefaultWindow)
	w.holdLease(issue, "agent-intruder")

	p := w.pipeline()
	if _, err := p.Recover(ctx, issue, testUnit); !core.HasCode(err, core.CodeLeaseHeld) {
		t.Fatalf("Recover under another holder's lease = %v, want LEASE_HELD", err)
	}
	rollbacks := actions("release.rollback")
	if len(rollbacks) != 1 || rollbacks[0]["without_lease"] != "true" || rollbacks[0]["restored_to"] != string(lkgDigest) {
		t.Fatalf("the rollback without the lease was audited as %+v", rollbacks)
	}

	w.clock.Advance(time.Hour)
	if out, err := p.Recover(ctx, issue, testUnit); err != nil || out.Result != ResultBlocked {
		t.Fatalf("Recover once the lease is free = %+v, %v; want blocked", out, err)
	}
	if report, found := w.recoveryReport(run); !found || report.RestoredTo != w.serving().Digest || report.RestoredTo != lkgDigest {
		t.Fatalf("the recovery report %+v (found %v) does not name what serves, %s", report, found, w.serving().Digest)
	}
}

// A release an earlier build recorded as the LKG before its Verified step
// is verified by the recovery from what the running system answers, and
// that is audited.
func TestTheRecoveryThatVerifiesAReleaseIsAudited(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	actions := w.auditActions()
	issue, run := issueRef(224), core.RunID("run-224")
	w.seedInFlight(issue, run, firstBuild, core.StateMonitoring)
	req := w.request(issue, run, firstBuild)
	if err := w.book.record(verifiedRelease{issue: issue, run: run, artifact: req.Artifact, receipt: req.Receipt, verifiedAt: w.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if out, err := w.pipeline().Recover(ctx, issue, testUnit); err != nil || out.State != core.StateVerified {
		t.Fatalf("Recover = %+v, %v; want the release verified", out, err)
	}
	verified := actions("release.verify_recovered")
	if len(verified) != 1 || verified[0]["release"] != string(firstBuild) || verified[0]["observed"] != string(firstBuild) {
		t.Fatalf("the recovery's verification was audited as %+v", verified)
	}
}

// A verified release that cannot be judged is blocked with an audit entry
// that names the release and what it was restored to.
func TestTheBlockOfAnUnjudgedVerifiedReleaseIsAudited(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Lander = &countingLander{}
	actions := w.auditActions()
	unlock := w.lockTrustVolume()
	issue, run := issueRef(225), core.RunID("run-225")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	if out, err := w.pipeline().Release(ctx, req); err == nil || out.State != core.StateVerified {
		t.Fatalf("Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	unlock()
	w.backend.mu.Lock()
	w.backend.unhealthy[core.ReleaseDigest(firstBuild)] = true
	w.backend.mu.Unlock()

	out, err := w.pipeline().FinishVerified(ctx, req)
	if err != nil || out.Result != ResultBlocked || out.Cause != statemachine.BlockReleaseUnjudged {
		t.Fatalf("FinishVerified = %+v, %v; want blocked as not judged", out, err)
	}
	unjudged := actions("release.unjudged")
	if len(unjudged) != 1 || unjudged[0]["release"] != string(firstBuild) || unjudged[0]["restored_to"] != string(lkgDigest) {
		t.Fatalf("the block was audited as %+v", unjudged)
	}
	if b, found := w.block(run); !found || !b.Verified || b.Cause != statemachine.BlockReleaseUnjudged {
		t.Fatalf("the block record is %+v (found %v), want a verified release not judged", b, found)
	}
}

// At startup a running system that does not answer records nothing and
// writes no step: the error is reported for the pass to log, and the
// release is judged on a later pass.
func TestAStartupThatCannotObserveTheRunningSystemRecordsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	unlock := w.lockTrustVolume()
	issue, run := issueRef(226), core.RunID("run-226")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	if out, err := w.pipeline().Release(ctx, req); err == nil || out.State != core.StateVerified {
		t.Fatalf("Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	unlock()
	w.backend.mu.Lock()
	w.backend.observeErr = core.NewError(core.CodeUnavailable, "the edge does not answer")
	w.backend.mu.Unlock()
	before := w.record(issue)

	if err := w.pipeline().RecordVerifiedLKG(ctx, req); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("RecordVerifiedLKG while nothing answers = %v, want UNAVAILABLE", err)
	}
	if lkg := w.lkg(); lkg.Digest != lkgDigest {
		t.Fatalf("the LKG is %s, want the one before the release", lkg.Digest)
	}
	if after := w.record(issue); after != before {
		t.Fatalf("the record moved from %+v to %+v; startup writes no step", before, after)
	}
}

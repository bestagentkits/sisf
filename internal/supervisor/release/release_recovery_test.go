package release

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/state"
)

// seedInFlight writes the lifecycle of issue on to the state a supervisor
// that stopped mid-release left it in, under the issue lease that
// supervisor took and never gave up, and puts the deployment backend where
// that release left it: the release on part or all of the traffic, or, for
// a record left in RolledBack, the LKG serving again.
func (w *world) seedInFlight(issue core.IssueRef, run core.RunID, build core.Digest, left core.State) {
	w.t.Helper()
	w.seedReleaseCandidate(issue, run, build)
	ctx := context.Background()
	release := core.ReleaseDigest(build)
	events := map[core.State][]core.Event{
		core.StateCanary:     {core.EventRolloutStarted},
		core.StateMonitoring: {core.EventRolloutStarted, core.EventRolloutHealthy},
		core.StateRolledBack: {core.EventRolloutStarted, core.EventErrorThresholdExceeded},
	}[left]
	err := w.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		lease, err := tx.AcquireLease(ctx, state.IssueLeaseResource(issue), DefaultLeaseOwner, 3*DefaultWindow)
		if err != nil {
			return err
		}
		for _, event := range events {
			record, err := tx.GetRecord(ctx, issue)
			if err != nil {
				return err
			}
			to, err := statemachine.Next(record.State, event)
			if err != nil {
				return err
			}
			t := core.Transition{
				IssueID: issue, From: record.State, To: to, Event: event, ExpectedVersion: record.Version,
				IdempotencyKey: "release/" + string(run) + "/" + string(event), RunID: run, Attempt: record.Attempt,
				LeaseOwner: lease.Owner, FencingToken: lease.Token, ActorID: actorID,
			}
			switch event {
			case core.EventRolloutStarted:
				t.CandidateDigest, t.ReleaseDigest = build, release
			case core.EventRolloutHealthy:
				t.ReleaseDigest = release
			}
			if _, err := tx.Transition(ctx, t); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		w.t.Fatalf("seed %s in %s: %v", issue, left, err)
	}
	if _, err := w.backend.Stage(ctx, w.keys.artifactFor(w.t, build)); err != nil {
		w.t.Fatal(err)
	}
	rev := deploy.Revision{Unit: testUnit, Digest: release}
	switch left {
	case core.StateCanary:
		_ = w.backend.Promote(ctx, rev, 5)
	case core.StateMonitoring:
		_ = w.backend.Promote(ctx, rev, 100)
	}
	w.backend.mu.Lock()
	w.backend.staged, w.backend.promotions = nil, nil
	w.backend.mu.Unlock()
}

// requireBlockedWith fails the test unless the issue of run is Blocked,
// with its recovery report and its block both naming cause.
func requireBlockedWith(t *testing.T, w *world, issue core.IssueRef, run core.RunID, cause statemachine.BlockCause) {
	t.Helper()
	w.requireState(issue, core.StateBlocked)
	report, found := w.recoveryReport(run)
	if !found || report.Cause != cause || report.RestoredTo != lkgDigest || !report.DataUntouched {
		t.Fatalf("recovery report %+v (found %v), want the LKG restored with cause %s", report, found, cause)
	}
	b, found := w.block(run)
	if !found || b.Cause != cause || b.IssueID != issue || b.RestoredTo != lkgDigest || b.Code != "" {
		t.Fatalf("block %+v (found %v), want cause %s", b, found, cause)
	}
}

// requireRolledBackThenBlocked fails the test unless issue, which stood at
// version before in Canary or Monitoring, took exactly two steps since:
// the one to RolledBack and the one to Blocked.
func requireRolledBackThenBlocked(t *testing.T, w *world, issue core.IssueRef, before int64) {
	t.Helper()
	if record := w.record(issue); record.State != core.StateBlocked || record.Version != before+2 {
		t.Fatalf("the record is %s at version %d, want Blocked two steps after version %d", record.State, record.Version, before)
	}
}

// A supervisor that stopped while a release was in Canary or Monitoring
// left it serving with nothing watching it. A new pipeline over the same
// store recovers it: the LKG is restored once, the issue goes RolledBack
// then Blocked as interrupted, and the recovery report is written. Doing it
// again finds nothing to do.
func TestARestartedSupervisorRestoresTheLKGOfAnUnwatchedRelease(t *testing.T) {
	t.Parallel()
	for i, left := range []core.State{core.StateCanary, core.StateMonitoring} {
		t.Run(string(left), func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			issue, run := issueRef(200+i), core.RunID("run-restart")
			w.seedInFlight(issue, run, firstBuild, left)

			before := w.record(issue).Version
			restarted := w.pipeline()
			out, err := restarted.Recover(context.Background(), issue, testUnit)
			if err != nil || out.Result != ResultBlocked || out.Cause != statemachine.BlockReleaseInterrupted || out.State != core.StateBlocked {
				t.Fatalf("Recover = %+v, %v; want blocked as interrupted", out, err)
			}
			requireBlockedWith(t, w, issue, run, statemachine.BlockReleaseInterrupted)
			requireRolledBackThenBlocked(t, w, issue, before)

			again, err := restarted.Recover(context.Background(), issue, testUnit)
			if err != nil || again.Result != "" || again.State != core.StateBlocked {
				t.Fatalf("a second recovery = %+v, %v; want nothing to do", again, err)
			}
			if _, rollbacks, _, data := w.backend.snapshot(); len(rollbacks) != 1 || rollbacks[0] != lkgDigest || data != "customer-data-v1" {
				t.Fatalf("rollbacks %v with data %q, want one restore of the LKG and the data untouched", rollbacks, data)
			}
		})
	}
}

// A restore that switched back to the LKG but could not block the issue
// left it in RolledBack. The recovery finishes it.
func TestARecoveryFinishesARestoreThatCouldNotBlockTheIssue(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	issue, run := issueRef(210), core.RunID("run-stranded")
	w.seedInFlight(issue, run, firstBuild, core.StateRolledBack)

	out, err := w.pipeline().Recover(context.Background(), issue, testUnit)
	if err != nil || out.Result != ResultBlocked {
		t.Fatalf("Recover = %+v, %v; want blocked", out, err)
	}
	requireBlockedWith(t, w, issue, run, statemachine.BlockReleaseInterrupted)
}

// Recoveries that race over one issue restore the LKG once.
func TestConcurrentRecoveriesOfOneReleaseRestoreTheLKGOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	issue, run := issueRef(211), core.RunID("run-raced")
	w.seedInFlight(issue, run, firstBuild, core.StateCanary)
	p := w.pipeline()

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := p.Recover(context.Background(), issue, testUnit); err != nil && !core.HasCode(err, core.CodeLeaseHeld) {
				t.Errorf("Recover: %v", err)
			}
		})
	}
	wg.Wait()
	requireBlockedWith(t, w, issue, run, statemachine.BlockReleaseInterrupted)
	if _, rollbacks, _, _ := w.backend.snapshot(); len(rollbacks) != 1 {
		t.Fatalf("rollbacks %v, want exactly one", rollbacks)
	}
}

// A release whose rollout started and that cannot go on, here because its
// issue lease lapsed in the middle of the canary, is restored and blocked
// as interrupted, never held in Canary with traffic on it.
func TestARolloutThatCannotGoOnIsRestoredAndBlockedNotHeld(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	issue, run := issueRef(212), core.RunID("run-lapsed")
	w.seedReleaseCandidate(issue, run, firstBuild)
	var once sync.Once
	w.backend.onMeasure = func(rev core.ReleaseDigest) {
		if rev == core.ReleaseDigest(firstBuild) {
			once.Do(func() { w.clock.Advance(2 * time.Hour) })
		}
	}

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	if err != nil || out.Result != ResultBlocked || out.Cause != statemachine.BlockReleaseInterrupted {
		t.Fatalf("Release = %+v, %v; want restored and blocked as interrupted", out, err)
	}
	requireBlockedWith(t, w, issue, run, statemachine.BlockReleaseInterrupted)
	if _, rollbacks, _, _ := w.backend.snapshot(); len(rollbacks) != 1 || rollbacks[0] != lkgDigest {
		t.Fatalf("rollbacks %v, want one restore of the LKG", rollbacks)
	}
}

// A supervisor that stops in the middle of a rollout leaves the release as
// it is and says so; the recovery at its next start restores the LKG.
func TestAReleaseStoppedWithItsSupervisorIsRecoveredAtTheNextStart(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	issue, run := issueRef(213), core.RunID("run-stopped")
	w.seedReleaseCandidate(issue, run, firstBuild)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.backend.onMeasure = func(rev core.ReleaseDigest) {
		if rev == core.ReleaseDigest(firstBuild) {
			cancel()
		}
	}

	out, err := w.pipeline().Release(ctx, w.request(issue, run, firstBuild))
	if err == nil || out.Result != ResultInterrupted || out.State != core.StateCanary {
		t.Fatalf("Release = %+v, %v; want interrupted in Canary", out, err)
	}
	if _, rollbacks, _, _ := w.backend.snapshot(); len(rollbacks) != 0 {
		t.Fatalf("a stopping supervisor rolled back: %v", rollbacks)
	}
	w.backend.onMeasure = nil
	recovered, err := w.pipeline().Recover(context.Background(), issue, testUnit)
	if err != nil || recovered.Result != ResultBlocked {
		t.Fatalf("Recover = %+v, %v; want blocked", recovered, err)
	}
	requireBlockedWith(t, w, issue, run, statemachine.BlockReleaseInterrupted)
}

// A live release of the pipeline is never recovered under it.
func TestARecoveryLeavesAReleaseThePipelineDrivesAlone(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	issue, run := issueRef(214), core.RunID("run-driven")
	w.seedReleaseCandidate(issue, run, firstBuild)
	p := w.pipeline()
	var once sync.Once
	var during Outcome
	w.backend.onMeasure = func(core.ReleaseDigest) {
		once.Do(func() {
			var err error
			if during, err = p.Recover(context.Background(), issue, testUnit); err != nil {
				t.Errorf("Recover during the release: %v", err)
			}
		})
	}
	out, err := p.Release(context.Background(), w.request(issue, run, firstBuild))
	if err != nil || out.Result != ResultClosed {
		t.Fatalf("Release = %+v, %v; want closed", out, err)
	}
	if during.Result != "" {
		t.Fatalf("the recovery acted on a release the pipeline drove: %+v", during)
	}
}

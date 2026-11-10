package release

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// requireRestored fails the test unless the release was rolled back to the
// LKG, observed running again, the issue blocked with a recovery report and
// the data left as it was.
func requireRestored(t *testing.T, w *world, issue core.IssueRef, run core.RunID, out Outcome, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("Release: %v (outcome %+v)", err, out)
	}
	if out.Result != ResultBlocked || out.RestoredTo != lkgDigest || out.Observed.Digest != lkgDigest || !out.Observed.Healthy {
		t.Fatalf("outcome %s restored to %s observing %+v, want blocked with the LKG observed running", out.Result, out.RestoredTo, out.Observed)
	}
	w.requireState(issue, core.StateBlocked)
	_, rollbacks, _, data := w.backend.snapshot()
	if len(rollbacks) != 1 || rollbacks[0] != lkgDigest {
		t.Fatalf("rollbacks %v, want one to the LKG", rollbacks)
	}
	if data != "customer-data-v1" {
		t.Fatalf("the data volume changed to %q during the rollback", data)
	}
	report, found := w.recoveryReport(run)
	if !found || report.RestoredTo != lkgDigest || report.Observed.Digest != lkgDigest || !report.DataUntouched || report.Reason == "" {
		t.Fatalf("recovery report %+v (found %v), want the restore and its evidence", report, found)
	}
	if lkg := w.lkg(); lkg.Digest != lkgDigest {
		t.Fatalf("a rolled-back release became the LKG: %+v", lkg)
	}
	if len(w.publisher.entries) != 0 {
		t.Fatalf("a rolled-back release was published: %+v", w.publisher.entries)
	}
}

func TestAFailingCanaryIsRolledBackToTheLKGWithinTwoWindows(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.backend.errorRate[core.ReleaseDigest(firstBuild)] = 0.10
	issue, run := issueRef(10), core.RunID("run-10")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	requireRestored(t, w, issue, run, out, err)
	if len(out.Windows) != 2 || !out.Windows[0].Breach || !out.Windows[1].Breach {
		t.Fatalf("windows %+v, want two breaching windows in a row", out.Windows)
	}
	if promotions, _, _, _ := w.backend.snapshot(); len(promotions) != 2 || promotions[1] != 25 {
		t.Fatalf("promotions %v, want the canary stopped before it took all traffic", promotions)
	}
	var queued []state.OutboxMessage
	err = w.store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		queued, err = tx.PendingOutbox(ctx, 10)
		return err
	})
	if err != nil || len(queued) != 1 || queued[0].Topic != TopicRecoveryReport {
		t.Fatalf("outbox %+v (%v), want the recovery report queued for the issue", queued, err)
	}
}

func TestARegressionAfterTheReleaseTookAllTrafficIsRolledBack(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	delete(w.backend.caps, core.CapDeployCanary)
	w.backend.errorRate[core.ReleaseDigest(firstBuild)] = 0.05
	issue, run := issueRef(11), core.RunID("run-11")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	requireRestored(t, w, issue, run, out, err)
}

func TestACriticalHealthFailureRollsBackAtOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.backend.critical[core.ReleaseDigest(firstBuild)] = 1
	issue, run := issueRef(12), core.RunID("run-12")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	requireRestored(t, w, issue, run, out, err)
	if len(out.Windows) != 1 {
		t.Fatalf("windows %+v, want the rollback after the first window", out.Windows)
	}
}

func TestOneBreachingWindowAloneDoesNotRollBack(t *testing.T) {
	t.Parallel()
	m := NewMonitor(MonitorConfig{})
	bad := metrics(1000, 100, true)
	good := metrics(1000, 0, true)
	for i, step := range []struct {
		m    func() (WindowReport, Decision)
		want Decision
	}{
		{func() (WindowReport, Decision) { return m.Judge(bad, 0) }, DecisionContinue},
		{func() (WindowReport, Decision) { return m.Judge(good, 0) }, DecisionContinue},
		{func() (WindowReport, Decision) { return m.Judge(bad, 0) }, DecisionContinue},
		{func() (WindowReport, Decision) { return m.Judge(bad, 0) }, DecisionRollback},
	} {
		if _, got := step.m(); got != step.want {
			t.Fatalf("window %d decided %s, want %s", i+1, got, step.want)
		}
	}
}

func TestARestoreThatCannotBeObservedIsNotReportedAsRolledBack(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.backend.errorRate[core.ReleaseDigest(firstBuild)] = 0.10
	w.backend.rollbackErr = core.NewError(core.CodeUnavailable, "the edge does not answer")
	issue, run := issueRef(13), core.RunID("run-13")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	if !core.HasCode(err, core.CodeReleaseHold) || out.Result != ResultRestoreFailed {
		t.Fatalf("Release = %s, %v; want restore_failed with RELEASE_HOLD", out.Result, err)
	}
	w.requireState(issue, core.StateCanary)
	if _, found := w.recoveryReport(run); found {
		t.Fatal("a recovery report was written for a restore that did not happen")
	}
}

// freezeAfter reports frozen from its n-th question on, as an emergency
// stop that arrives while a release is in flight.
type freezeAfter struct {
	asked atomic.Int32
	n     int32
}

func (f *freezeAfter) Frozen(context.Context) (bool, error) { return f.asked.Add(1) >= f.n, nil }

func TestAnEmergencyStopDuringTheRolloutRestoresTheLKG(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.cfg.Freeze = &freezeAfter{n: 3}
	issue, run := issueRef(14), core.RunID("run-14")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	requireRestored(t, w, issue, run, out, err)
	if !strings.Contains(out.Reason, "emergency stop") {
		t.Fatalf("reason %q, want the emergency stop", out.Reason)
	}
}

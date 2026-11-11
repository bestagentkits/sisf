package release

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// lowTrafficWorld is a world whose application serves far fewer requests
// per window than a conclusion needs, with a synthetic suite.
func lowTrafficWorld(t *testing.T, pass bool) (*world, *fakeSynthetic) {
	t.Helper()
	w := newWorld(t)
	w.backend.totalPerWindow = 20
	suite := &fakeSynthetic{pass: pass}
	w.cfg.Synthetic = suite
	return w, suite
}

func TestALowTrafficReleaseIsHeldOnSyntheticChecksBeforeItIsVerified(t *testing.T) {
	t.Parallel()
	w, suite := lowTrafficWorld(t, true)
	issue, run := issueRef(60), core.RunID("run-60")
	w.seedReleaseCandidate(issue, run, firstBuild)
	began := w.clock.Now()

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	if err != nil || out.Result != ResultClosed {
		t.Fatalf("Release = %s, %v; want closed after the synthetic hold", out.Result, err)
	}
	monitoring := 0
	for _, window := range out.Windows {
		if window.Decision != DecisionLowTraffic || window.Synthetic == nil || !window.Synthetic.Passed {
			t.Fatalf("window %+v, want every window judged by a passing synthetic run", window)
		}
		if window.Percent == 100 {
			monitoring++
		}
	}
	held := DefaultLowTrafficMinHold / DefaultWindow
	if monitoring < int(held) || int(suite.runs.Load()) != len(out.Windows) {
		t.Fatalf("%d windows at full traffic and %d synthetic runs, want the hold to last %d windows", monitoring, suite.runs.Load(), held)
	}
	if elapsed := w.clock.Now().Sub(began); elapsed < DefaultLowTrafficMinHold {
		t.Fatalf("the release was verified after %s, before the %s hold ended", elapsed, DefaultLowTrafficMinHold)
	}
}

func TestAFailedSyntheticCheckRestoresTheLKG(t *testing.T) {
	t.Parallel()
	w, _ := lowTrafficWorld(t, false)
	issue, run := issueRef(61), core.RunID("run-61")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	requireRestored(t, w, issue, run, out, err)
}

func TestAFailedRealRequestExtendsTheHoldInsteadOfRollingBack(t *testing.T) {
	t.Parallel()
	w, _ := lowTrafficWorld(t, true)
	w.backend.errorRate[core.ReleaseDigest(firstBuild)] = 0.5
	issue, run := issueRef(62), core.RunID("run-62")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	requireRestored(t, w, issue, run, out, err)
	extended := 0
	for _, window := range out.Windows {
		if strings.Contains(window.Note, "extends the hold") {
			extended++
		}
	}
	if extended != DefaultMaxHoldExtensions+1 || !strings.Contains(out.Reason, "kept failing") {
		t.Fatalf("%d extensions, reason %q; want the hold extended %d times before the restore", extended, out.Reason, DefaultMaxHoldExtensions)
	}
}

// brokenSynthetic is a synthetic suite that judges nothing: it cannot be
// run, and says so before a release when asked.
type brokenSynthetic struct {
	notReady error
	runs     atomic.Int32
}

func (s *brokenSynthetic) Run(context.Context, deploy.Revision) (SyntheticResult, error) {
	s.runs.Add(1)
	return SyntheticResult{}, core.NewError(core.CodeUnavailable, "the synthetic suite could not be started")
}

func (s *brokenSynthetic) Ready(context.Context) error { return s.notReady }

// A synthetic suite that cannot be run before the rollout starts holds the
// release: nothing is deployed and the issue waits in ReleaseCandidate.
func TestALowTrafficReleaseHoldsWhenItsSyntheticSuiteCannotBeRun(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.backend.totalPerWindow = 20
	w.cfg.Synthetic = &brokenSynthetic{notReady: core.NewError(core.CodeNotFound, "the synthetic program is gone")}
	issue, run := issueRef(62), core.RunID("run-62")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	if !core.HasCode(err, core.CodeReleaseHold) || out.Result != ResultHeld || out.State != core.StateReleaseCandidate {
		t.Fatalf("Release = %+v, %v; want held in ReleaseCandidate", out, err)
	}
	if promotions, rollbacks, staged, _ := w.backend.snapshot(); len(promotions)+len(rollbacks)+len(staged) != 0 {
		t.Fatalf("a release that could not be judged still acted: staged %v promoted %v rolled back %v", staged, promotions, rollbacks)
	}
}

// A synthetic suite that cannot be run once the rollout started judged
// nothing: the LKG is restored and the issue blocked as a release that
// could not be judged, not as one a synthetic check failed.
func TestASyntheticSuiteThatCannotRunDuringTheRolloutRestoresTheLKGAsUnjudged(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.backend.totalPerWindow = 20
	suite := &brokenSynthetic{}
	w.cfg.Synthetic = suite
	issue, run := issueRef(63), core.RunID("run-63")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	requireRestored(t, w, issue, run, out, err)
	if out.Cause != statemachine.BlockReleaseUnjudged || strings.Contains(out.Reason, "a synthetic check failed") || suite.runs.Load() != 1 {
		t.Fatalf("outcome cause %s reason %q after %d runs, want a release that could not be judged", out.Cause, out.Reason, suite.runs.Load())
	}
	requireBlockedWith(t, w, issue, run, statemachine.BlockReleaseUnjudged)
}

// A failed synthetic check is a judgment: the release is rolled back as
// unhealthy.
func TestAFailedSyntheticCheckBlocksTheIssueAsRolledBack(t *testing.T) {
	t.Parallel()
	w, _ := lowTrafficWorld(t, false)
	issue, run := issueRef(64), core.RunID("run-64")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	requireRestored(t, w, issue, run, out, err)
	requireBlockedWith(t, w, issue, run, statemachine.BlockRolledBack)
}

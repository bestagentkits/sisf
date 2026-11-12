package release

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// requireHeldUntouched fails the test unless the release held with code,
// the issue is still a release candidate and nothing was staged, promoted
// or rolled back.
func requireHeldUntouched(t *testing.T, w *world, issue core.IssueRef, out Outcome, err error, code core.ErrorCode) {
	t.Helper()
	if !core.HasCode(err, code) || out.Result != ResultHeld {
		t.Fatalf("Release = %s, %v; want held with %s", out.Result, err, code)
	}
	w.requireState(issue, core.StateReleaseCandidate)
	promotions, rollbacks, staged, _ := w.backend.snapshot()
	if len(promotions)+len(rollbacks)+len(staged) != 0 {
		t.Fatalf("a held release still acted: staged %v promoted %v rolled back %v", staged, promotions, rollbacks)
	}
}

func TestAReleaseHoldsWhenItCannotBeObservedOrRolledBack(t *testing.T) {
	t.Parallel()
	unavailable := core.NewError(core.CodeUnavailable, "the backend does not answer")
	cases := map[string]func(w *world){
		"no metrics for the running release":    func(w *world) { w.backend.measureErr[lkgDigest] = unavailable },
		"the adapter cannot say what it can do": func(w *world) { w.backend.capsErr = unavailable },
		"the adapter cannot observe what runs":  func(w *world) { w.backend.observeErr = unavailable },
		"the adapter has no rollback":           func(w *world) { delete(w.backend.caps, core.CapDeployRollback) },
		"the adapter has no metrics":            func(w *world) { delete(w.backend.caps, core.CapDeployMetrics) },
		"rollback is only claimed": func(w *world) {
			w.backend.caps[core.CapDeployRollback] = core.SupportUnverified
		},
		"the LKG is not what runs": func(w *world) { w.backend.runningInstead = core.ReleaseDigest(otherBuild) },
		"the LKG is unhealthy":     func(w *world) { w.backend.unhealthy[lkgDigest] = true },
		"no LKG was ever recorded": func(w *world) {
			if err := os.Remove(filepath.Join(w.trustDir, "lkg.json")); err != nil {
				w.t.Fatalf("remove LKG record: %v", err)
			}
		},
		"the LKG entry was tampered with": func(w *world) {
			if err := os.WriteFile(filepath.Join(w.trustDir, "lkg-unit-app.json"), []byte(`{"unit":"app"}`), 0o600); err != nil {
				w.t.Fatalf("write LKG entry: %v", err)
			}
		},
		"real traffic is too low and there is no synthetic suite": func(w *world) { w.backend.totalPerWindow = 20 },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			issue, run := issueRef(20), core.RunID("run-20")
			w.seedReleaseCandidate(issue, run, firstBuild)
			breakIt(w)
			out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
			requireHeldUntouched(t, w, issue, out, err, core.CodeReleaseHold)
		})
	}
}

func TestAReleaseHoldsWhileAnEmergencyStopFreezesPublishing(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.freeze.frozen.Store(true)
	issue, run := issueRef(21), core.RunID("run-21")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	requireHeldUntouched(t, w, issue, out, err, core.CodeFrozen)
}

func TestAReleaseHoldsWhileTheRunStillHoldsTheIssue(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	issue, run := issueRef(22), core.RunID("run-22")
	w.seedReleaseCandidate(issue, run, firstBuild)
	w.holdLease(issue, "dispatcher-2")

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	requireHeldUntouched(t, w, issue, out, err, core.CodeLeaseHeld)
}

func TestAStagingFailureLeavesUsersOnTheLKG(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.backend.stageErr = core.NewError(core.CodeUnavailable, "the image cannot be pulled")
	issue, run := issueRef(23), core.RunID("run-23")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	requireHeldUntouched(t, w, issue, out, err, core.CodeReleaseHold)
}

func TestLosingTheMetricsDuringTheRolloutRestoresTheLKGInsteadOfAssumingSuccess(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.backend.measureErr[core.ReleaseDigest(firstBuild)] = core.NewError(core.CodeUnavailable, "the edge counters are gone")
	issue, run := issueRef(24), core.RunID("run-24")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	requireRestored(t, w, issue, run, out, err)
}

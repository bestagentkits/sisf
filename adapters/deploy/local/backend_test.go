package local_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/deploy/local"
	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

func observed(t *testing.T, w *world) deploy.Observed {
	t.Helper()
	obs, err := w.backend.ObservedRevision(context.Background(), "app")
	if err != nil {
		t.Fatalf("ObservedRevision: %v", err)
	}
	return obs
}

func TestBlueGreenAlternatesTheTwoSlotsAndKeepsThePreviousRunning(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	first := w.release(digestA)
	if first.ID != "blue" || !w.alive(0) || w.alive(1) {
		t.Fatalf("first revision on %q, blue alive %v green alive %v", first.ID, w.alive(0), w.alive(1))
	}
	second := w.release(digestB)
	if second.ID != "green" || !w.alive(0) || !w.alive(1) {
		t.Fatalf("second revision on %q, want green with blue kept as the way back", second.ID)
	}
	if seen := w.traffic(10); seen[string(digestB)] != 10 {
		t.Fatalf("traffic = %v, want it all on the new revision", seen)
	}
	third := w.stage(digestC) // takes the idle slot, which held the first revision
	if third.ID != "blue" || w.starterApp(0) == nil {
		t.Fatalf("third revision on %q", third.ID)
	}
}

func (w *world) starterApp(slot int) *fakeProc { return w.starter.procs[w.addr(slot)] }

func TestStagingNeverTouchesTheEdgeOrTheRunningRevision(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	w.release(digestA)
	w.stage(digestB)
	if split, _ := w.splitter.Split(context.Background()); split.Stable.Revision != string(digestA) {
		t.Fatalf("staging moved the edge: %+v", split)
	}
	if seen := w.traffic(5); seen[string(digestA)] != 5 {
		t.Fatalf("traffic = %v, want the stable to keep serving", seen)
	}
}

func TestOnlyAWholeSwitchIsAcceptedAndCanaryIsNotDeclared(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	rev := w.stage(digestA)
	for _, percent := range []int{1, 50, 99, 0, 101} {
		if err := w.backend.Promote(context.Background(), rev, percent); !core.HasCode(err, core.CodeNotSupported) {
			t.Errorf("Promote(%d): %v, want NOT_SUPPORTED", percent, err)
		}
	}
	if split, _ := w.splitter.Split(context.Background()); split.Stable.Revision != "bootstrap" {
		t.Fatalf("a refused promotion moved the edge: %+v", split)
	}
	caps, _ := w.backend.Capabilities(context.Background())
	if !deploy.SelfPublishAllowed(caps) || caps.Supports(core.CapDeployCanary) || !caps.Supports(core.CapDeployBlueGreen) {
		t.Fatalf("capabilities = %v, want blue-green without canary", caps)
	}
}

func TestStageRefusesAnArtifactNotMadeForThisFactoryBeforeStartingAnything(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	for name, mutate := range map[string]func(*deploy.Artifact){
		"another factory": func(a *deploy.Artifact) { a.FactoryID = "other-app" },
		"bad unit":        func(a *deploy.Artifact) { a.Unit = "App; rm" },
		"unsigned":        func(a *deploy.Artifact) { a.Signature = core.Signature{} },
	} {
		a := artifact(digestA)
		mutate(&a)
		if _, err := w.backend.Stage(context.Background(), a); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if w.starter.started != 0 {
		t.Fatalf("%d processes started for refused artifacts", w.starter.started)
	}
}

func TestAStagedApplicationThatNamesAnotherRevisionHoldsAndIsStopped(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	w.starter.say[w.addr(0)] = string(digestB)
	if _, err := w.backend.Stage(context.Background(), artifact(digestA)); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Stage: %v, want RELEASE_HOLD", err)
	}
	if w.alive(0) {
		t.Fatal("the failed revision is still running")
	}
}

func TestAStagedApplicationThatNeverBecomesHealthyHoldsWhenTheTimeIsUp(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{timeout: 10 * time.Second})
	w.starter.set(w.starter.down, w.addr(0))
	start := w.clock.Now()
	if _, err := w.backend.Stage(context.Background(), artifact(digestA)); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Stage: %v, want RELEASE_HOLD", err)
	}
	if w.clock.Now().Sub(start) < 10*time.Second || w.alive(0) {
		t.Fatal("it should wait out the timeout and then stop the process")
	}
}

func TestObservedRevisionIsWhatRunsAndNotWhatWasAsked(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	rev := w.release(digestA)
	if obs := observed(t, w); !obs.Matches(rev) {
		t.Fatalf("observed = %+v, want %+v", obs, rev)
	}

	w.starter.mu.Lock()
	w.starter.say[w.addr(0)] = string(digestB) // the process now answers as another revision
	w.starter.mu.Unlock()
	if obs := observed(t, w); obs.Matches(rev) || obs.Healthy || obs.Digest != digestB {
		t.Fatalf("observed = %+v, want the application's own answer and not healthy for A", obs)
	}
}

func TestADeadProcessAnUnhealthyApplicationAndAMisdirectedEdgeDoNotMatch(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	rev := w.release(digestA)

	w.starter.set(w.starter.down, w.addr(0))
	if obs := observed(t, w); obs.Matches(rev) {
		t.Fatalf("an unhealthy application matched: %+v", obs)
	}
	w.starter.mu.Lock()
	delete(w.starter.down, w.addr(0))
	w.starter.mu.Unlock()
	if !observed(t, w).Matches(rev) {
		t.Fatal("a healthy application did not match")
	}
	// The edge keeps the label but sends traffic to the other slot.
	other := edge.Split{Stable: edge.Target{Revision: string(digestA), URL: "http://" + w.addr(1)}}
	if err := w.splitter.SetSplit(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if observed(t, w).Matches(rev) {
		t.Fatal("a misdirected edge matched")
	}
	if err := w.starterApp(0).Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if obs := observed(t, w); obs.Matches(rev) || obs.Digest != "" {
		t.Fatalf("a dead process matched: %+v", obs)
	}
}

func TestObservationHoldsWithoutAnEdgeAndIsEmptyWithoutAStable(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	if obs := observed(t, w); obs.Healthy || obs.Digest != "" {
		t.Fatalf("observed = %+v, want nothing running", obs)
	}
	down := newWorld(t, worldOptions{edge: func(edge.Controller) edge.Controller { return brokenEdge{} }})
	if _, err := down.backend.ObservedRevision(context.Background(), "app"); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("with the edge down: %v, want RELEASE_HOLD", err)
	}
	if _, err := w.backend.ObservedRevision(context.Background(), "../x"); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("malformed unit: %v", err)
	}
}

func TestPromoteRefusesARevisionThatLiesAndLeavesTheEdgeAlone(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	w.release(digestA)
	rev := w.stage(digestB)
	w.starter.mu.Lock()
	w.starter.say[w.addr(1)] = string(digestC)
	w.starter.mu.Unlock()
	if err := w.backend.Promote(context.Background(), rev, 100); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Promote: %v, want RELEASE_HOLD", err)
	}
	if split, _ := w.splitter.Split(context.Background()); split.Stable.Revision != string(digestA) {
		t.Fatalf("the edge moved: %+v", split)
	}
	if err := w.backend.Promote(context.Background(), deploy.Revision{Unit: "app", Digest: "x"}, 100); err == nil {
		t.Fatal("a malformed revision was promoted")
	}
}

func TestRollbackReturnsToThePreviousRevisionAndStopsTheOneItReplaces(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	first := w.release(digestA)
	w.release(digestB)
	if err := w.backend.Rollback(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if obs := observed(t, w); !obs.Matches(first) {
		t.Fatalf("observed = %+v, want %+v", obs, first)
	}
	if seen := w.traffic(5); seen[string(digestA)] != 5 || w.alive(1) {
		t.Fatalf("traffic %v, green alive %v, want all on the earlier revision and the other stopped", seen, w.alive(1))
	}
}

func TestRollbackHoldsWhenThePreviousRevisionWasReplacedOrIsNotWell(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	first := w.release(digestA)
	w.release(digestB)
	w.stage(digestC) // reuses the slot of the first revision
	if err := w.backend.Rollback(context.Background(), first); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Rollback to a replaced revision: %v, want RELEASE_HOLD", err)
	}
	if split, _ := w.splitter.Split(context.Background()); split.Stable.Revision != string(digestB) {
		t.Fatalf("a refused rollback moved the edge: %+v", split)
	}
	w.starter.set(w.starter.down, w.addr(1))
	if err := w.backend.Rollback(context.Background(), deploy.Revision{Unit: "app", Digest: digestB}); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Rollback to an unhealthy revision: %v, want RELEASE_HOLD", err)
	}
	if err := w.backend.Rollback(context.Background(), deploy.Revision{Unit: "app", Digest: "latest"}); err == nil {
		t.Fatal("a malformed revision was rolled back to")
	}
}

func TestMeasureReadsTheEdgeCountersAndTheProcessLog(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	rev := w.release(digestA)
	w.starter.set(w.starter.failPage, w.addr(0))
	w.traffic(20)
	w.starterApp(0).critical = 3

	m, err := w.backend.Measure(context.Background(), rev, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if m.Requests != 20 || m.ServerErrors != 20 || m.CriticalErrors != 3 || !m.Healthy || m.To.Sub(m.From) != 10*time.Minute {
		t.Fatalf("metrics = %+v", m)
	}
	other, err := w.backend.Measure(context.Background(), deploy.Revision{Unit: "app", Digest: digestB}, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := other.ErrorRate(); ok || other.Healthy {
		t.Fatalf("a revision that never ran: %+v, want no rate and not healthy", other)
	}
}

func TestMeasureHoldsWhenCountersCannotBeRead(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{edge: func(c edge.Controller) edge.Controller { return countsDown{c} }})
	rev := w.release(digestA)
	if _, err := w.backend.Measure(context.Background(), rev, time.Minute); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Measure: %v, want RELEASE_HOLD", err)
	}
	if _, err := w.backend.Measure(context.Background(), rev, 0); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("zero window: %v, want INVALID_ARGUMENT", err)
	}
}

func TestStageHoldsWhenTheEdgeCannotBeAsked(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{edge: func(edge.Controller) edge.Controller { return brokenEdge{} }})
	if _, err := w.backend.Stage(context.Background(), artifact(digestA)); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Stage with the edge down: %v, want RELEASE_HOLD", err)
	}
}

func TestMalformedConfigurationIsRefused(t *testing.T) {
	t.Parallel()
	good := func() local.Config {
		return local.Config{Factory: factory, Starter: &fakeStarter{}, Edge: brokenEdge{}}
	}
	if _, err := local.New(good()); err != nil {
		t.Fatalf("a valid configuration was refused: %v", err)
	}
	for name, mutate := range map[string]func(*local.Config){
		"no factory":           func(c *local.Config) { c.Factory = "" },
		"no starter":           func(c *local.Config) { c.Starter = nil },
		"no edge":              func(c *local.Config) { c.Edge = nil },
		"public host":          func(c *local.Config) { c.Host = "0.0.0.0" },
		"host name":            func(c *local.Config) { c.Host = "app.example" },
		"same port twice":      func(c *local.Config) { c.Ports = [2]int{7461, 7461} },
		"port out of range":    func(c *local.Config) { c.Ports = [2]int{7461, 70000} },
		"environment revision": func(c *local.Config) { c.Env = []string{"SISF_REVISION=x"} },
		"environment no value": func(c *local.Config) { c.Env = []string{"NAME"} },
	} {
		cfg := good()
		mutate(&cfg)
		if _, err := local.New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

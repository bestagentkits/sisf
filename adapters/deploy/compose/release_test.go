package compose_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

const tenMinutes = 10 * time.Minute

func TestACanarySendsExactlyItsShareAndMeasureReadsItsOwnCounters(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	stable := w.release(digestA)
	canary := w.stage(digestB)
	w.setFailPage(name(digestB))
	if err := w.backend.Promote(context.Background(), canary, 25); err != nil {
		t.Fatal(err)
	}

	if seen := w.traffic(100); seen[string(digestB)] != 25 || seen[string(digestA)] != 75 {
		t.Fatalf("traffic = %v, want 25 on the canary and 75 on the stable", seen)
	}
	bad, err := w.backend.Measure(context.Background(), canary, tenMinutes)
	if err != nil {
		t.Fatal(err)
	}
	if bad.Requests != 25 || bad.ServerErrors != 25 || !bad.Healthy || bad.Revision != canary {
		t.Fatalf("canary metrics = %+v, want 25 requests all failing", bad)
	}
	if rate, ok := bad.ErrorRate(); !ok || rate != 1 {
		t.Fatalf("canary error rate = %v, %v", rate, ok)
	}
	good, err := w.backend.Measure(context.Background(), stable, tenMinutes)
	if err != nil || good.Requests != 75 || good.ServerErrors != 0 {
		t.Fatalf("stable metrics = %+v, %v, want 75 clean requests", good, err)
	}
	if !good.To.After(good.From) || good.To.Sub(good.From) != tenMinutes {
		t.Errorf("window = %v to %v", good.From, good.To)
	}
}

func TestAnUnseenRevisionHasNoErrorRateRatherThanAZeroOne(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	rev := w.release(digestA)
	m, err := w.backend.Measure(context.Background(), rev, tenMinutes)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.ErrorRate(); ok {
		t.Fatalf("metrics = %+v, want no conclusion from no traffic", m)
	}
}

func TestMeasureCountsCriticalLogLinesAndReportsUnhealthyAsUnhealthy(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	w.mu.Lock()
	w.logs[name(digestA)] = "level=info msg=started\nlevel=critical msg=boom\n{\"level\":\"FATAL\",\"msg\":\"x\"}\nlevel=error msg=minor\n"
	w.mu.Unlock()
	rev := w.release(digestA)
	m, err := w.backend.Measure(context.Background(), rev, tenMinutes)
	if err != nil || m.CriticalErrors != 2 || !m.Healthy {
		t.Fatalf("metrics = %+v, %v, want 2 critical lines and healthy", m, err)
	}
	w.setDown(name(digestA), true)
	if m, _ := w.backend.Measure(context.Background(), rev, tenMinutes); m.Healthy {
		t.Fatalf("metrics = %+v, want not healthy", m)
	}
}

func TestMeasureHoldsWhenCountersCannotBeReadAndNeverReadsThemAsZero(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{edge: func(c edge.Controller) edge.Controller { return countsDown{c} }})
	rev := w.release(digestA)
	if _, err := w.backend.Measure(context.Background(), rev, tenMinutes); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Measure: %v, want RELEASE_HOLD", err)
	}
	for _, window := range []time.Duration{0, -time.Minute} {
		if _, err := w.backend.Measure(context.Background(), rev, window); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("window %v: %v, want INVALID_ARGUMENT", window, err)
		}
	}
	if _, err := w.backend.Measure(context.Background(), deploy.Revision{Unit: "app", Digest: "x"}, tenMinutes); err == nil {
		t.Error("a malformed revision was measured")
	}
}

func TestPromoteRefusesARevisionThatDoesNotRunAsItselfAndLeavesTheEdgeAlone(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	w.release(digestA)
	rev := w.stage(digestB)
	w.setSay(name(digestB), string(digestC)) // it lies after staging
	for _, percent := range []int{100, 10} {
		if err := w.backend.Promote(context.Background(), rev, percent); !core.HasCode(err, core.CodeReleaseHold) {
			t.Fatalf("Promote(%d): %v, want RELEASE_HOLD", percent, err)
		}
	}
	if split, _ := w.splitter.Split(context.Background()); split.Stable.Revision != string(digestA) || split.Canary != nil {
		t.Fatalf("the edge changed: %+v", split)
	}
}

func TestPromoteRefusesBadPercentagesAndASecondStableAsCanary(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	rev := w.release(digestA)
	for _, percent := range []int{-5, 0, 101} {
		if err := w.backend.Promote(context.Background(), rev, percent); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("Promote(%d): %v, want INVALID_ARGUMENT", percent, err)
		}
	}
	if err := w.backend.Promote(context.Background(), rev, 10); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a canary that is already the stable: %v, want INVALID_ARGUMENT", err)
	}
}

func TestPromotingToAHundredKeepsThePreviousRevisionAndRetiresOlderOnes(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	w.release(digestA)
	w.release(digestB)
	if got := w.containerNames(); !slices.Equal(got, []string{name(digestA), name(digestB)}) {
		t.Fatalf("containers = %v, want the previous revision kept as the way back", got)
	}
	w.release(digestC)
	if got := w.containerNames(); !slices.Equal(got, []string{name(digestB), name(digestC)}) {
		t.Fatalf("containers = %v, want the oldest retired", got)
	}
	for _, r := range w.daemon.Requests() {
		if r.Method == "DELETE" && strings.Contains(r.Query, "v=") {
			t.Fatalf("a removal asked for volumes to go: %s?%s", r.Path, r.Query)
		}
	}
}

func TestRollbackReturnsToTheEarlierRevisionAndStopsTheOneItReplaces(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	first := w.release(digestA)
	w.release(digestB)

	if err := w.backend.Rollback(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if obs := observed(t, w); !obs.Matches(first) {
		t.Fatalf("observed after the rollback = %+v, want %+v", obs, first)
	}
	if seen := w.traffic(10); seen[string(digestA)] != 10 {
		t.Fatalf("traffic = %v, want it all on the earlier revision", seen)
	}
	if w.running(name(digestB)) || !slices.Contains(w.containerNames(), name(digestB)) {
		t.Fatal("the replaced revision should be stopped and kept for diagnosis")
	}
}

func TestRollbackHoldsWhenThereIsNothingHealthyToReturnTo(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	w.release(digestA)
	w.release(digestB)
	w.release(digestC) // A is retired
	gone := deploy.Revision{Unit: "app", Digest: digestA}
	if err := w.backend.Rollback(context.Background(), gone); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Rollback to a removed revision: %v, want RELEASE_HOLD", err)
	}
	w.setSay(name(digestB), string(digestA)) // B no longer runs as itself
	if err := w.backend.Rollback(context.Background(), deploy.Revision{Unit: "app", Digest: digestB}); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Rollback to a revision that lies: %v, want RELEASE_HOLD", err)
	}
	if split, _ := w.splitter.Split(context.Background()); split.Stable.Revision != string(digestC) {
		t.Fatalf("a refused rollback moved the edge: %+v", split)
	}
	if err := w.backend.Rollback(context.Background(), deploy.Revision{Unit: "app", Digest: "latest"}); err == nil {
		t.Fatal("a malformed revision was rolled back to")
	}
}

func TestAnEdgeThatRefusesTheSwitchHoldsTheRelease(t *testing.T) {
	t.Parallel()
	down := newWorld(t, worldOptions{edge: func(edge.Controller) edge.Controller { return brokenEdge{} }})
	staged := down.stage(digestA)
	if err := down.backend.Promote(context.Background(), staged, 100); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Promote with the edge down: %v, want RELEASE_HOLD", err)
	}
	if err := down.backend.Rollback(context.Background(), staged); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Rollback with the edge down: %v, want RELEASE_HOLD", err)
	}
}

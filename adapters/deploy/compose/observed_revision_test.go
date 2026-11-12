package compose_test

import (
	"context"
	"testing"

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

func TestObservedRevisionIsWhatRunsAfterASwitch(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	rev := w.release(digestA)
	if obs := observed(t, w); !obs.Matches(rev) || obs.ObservedAt.IsZero() {
		t.Fatalf("observed = %+v, want %+v running and healthy", obs, rev)
	}
	next := w.release(digestB)
	if obs := observed(t, w); !obs.Matches(next) || obs.Matches(rev) {
		t.Fatalf("observed after the switch = %+v, want only %+v", obs, next)
	}
}

func TestAnAPIThatClaimsOneRevisionWhileTheApplicationRunsAnotherIsNotBelieved(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	rev := w.release(digestA)

	// Docker's label keeps saying A, but the process answering is B.
	w.setSay(name(digestA), string(digestB))
	obs := observed(t, w)
	if obs.Matches(rev) || obs.Healthy {
		t.Fatalf("observed = %+v, want the label alone not to prove revision A", obs)
	}
	if obs.Digest != digestB {
		t.Fatalf("observed digest = %q, want what the application says it is (%s)", obs.Digest, digestB)
	}
}

func TestAnApplicationThatClaimsARevisionTheLabelDoesNotNameIsNotBelievedEither(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	rev := w.release(digestA)

	// The API result is rewritten to name another revision; the application
	// still says A. Neither source alone is enough.
	w.setLabelLie(name(digestA), string(digestB))
	if obs := observed(t, w); obs.Matches(rev) || obs.Healthy {
		t.Fatalf("observed = %+v, want disagreement to be unhealthy", obs)
	}
}

func TestARunningContainerWhoseApplicationIsDownOrSilentIsNotHealthy(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	rev := w.release(digestA)
	w.setDown(name(digestA), true)
	if obs := observed(t, w); obs.Matches(rev) {
		t.Fatalf("observed = %+v, want an unhealthy application not to match", obs)
	}
	w.apps[name(digestA)].Close() // the process is gone while Docker still says running
	if obs := observed(t, w); obs.Matches(rev) || obs.Healthy {
		t.Fatalf("observed = %+v, want an unreachable application not to match", obs)
	}
}

func TestAStoppedContainerIsObservedAsNotRunningWithoutAnError(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	rev := w.release(digestA)
	if err := w.stopContainer(name(digestA)); err != nil {
		t.Fatal(err)
	}
	if obs := observed(t, w); obs.Matches(rev) || obs.Healthy {
		t.Fatalf("observed = %+v after a stop", obs)
	}
}

func TestAnEdgeThatPointsSomewhereElseIsNotTheRevisionItNames(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	rev := w.release(digestA)
	// The edge still labels its stable A but sends traffic to another application.
	if err := w.splitter.SetSplit(context.Background(), edgeSplit(string(digestA), w.resolve(name(digestC)))); err != nil {
		t.Fatal(err)
	}
	if obs := observed(t, w); obs.Matches(rev) {
		t.Fatalf("observed = %+v, want a mismatched edge target not to match", obs)
	}
}

func TestWhenNothingOfOursIsStableTheObservationIsEmptyNotAnInvention(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	if obs := observed(t, w); obs.Healthy || obs.Digest != "" || obs.Unit != "app" {
		t.Fatalf("observed = %+v, want nothing running", obs)
	}
}

func TestObservationHoldsWhenTheEdgeOrDockerCannotBeAsked(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	w.release(digestA)
	w.docker.Close()
	if _, err := w.backend.ObservedRevision(context.Background(), "app"); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("with Docker down: %v, want RELEASE_HOLD", err)
	}
	down := newWorld(t, worldOptions{edge: func(edge.Controller) edge.Controller { return brokenEdge{} }})
	if _, err := down.backend.ObservedRevision(context.Background(), "app"); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("with the edge down: %v, want RELEASE_HOLD", err)
	}
	if _, err := w.backend.ObservedRevision(context.Background(), "../etc"); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a malformed unit: %v, want INVALID_ARGUMENT", err)
	}
}

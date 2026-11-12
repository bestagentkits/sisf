package release

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// A backend that answers with an observation made before the rollout, such
// as one of an earlier release of the same digest, never proves the release
// healthy: the release cannot be observed, so the LKG is restored, and the
// release is neither published nor recorded as the LKG.
func TestAnObservationOlderThanTheRolloutNeverPublishesTheRelease(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	delete(w.backend.caps, core.CapDeployCanary)
	issue, run := issueRef(4), core.RunID("run-4")
	w.seedReleaseCandidate(issue, run, firstBuild)
	w.backend.observedAt = startTime.Add(-time.Hour)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if out.Result != ResultBlocked || out.RestoredTo != lkgDigest {
		t.Fatalf("outcome %s restored to %s, want blocked on the LKG", out.Result, out.RestoredTo)
	}
	w.requireState(issue, core.StateBlocked)
	if len(w.publisher.entries) != 0 {
		t.Fatalf("a release proven by a stale observation was published: %+v", w.publisher.entries)
	}
	if lkg := w.lkg(); lkg.Digest != lkgDigest {
		t.Fatalf("the LKG moved to %s on a stale observation", lkg.Digest)
	}
}

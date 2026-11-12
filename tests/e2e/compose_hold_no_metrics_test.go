//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// With no real traffic to judge by and no synthetic suite, the release of a
// candidate holds before anything is deployed: missing metrics are never
// taken for success, and nothing is promoted.
func TestComposeHoldsAReleaseWithoutMetrics(t *testing.T) {
	d := newComposeRelease(t)
	seedRelease(t, d, greetingDiff)
	before := servedRevision(t, d)
	// No load: the edge's counters of the stable revision stay below the
	// sample floor for the whole window before the release starts.
	_, issue := openBug(t, d, "Greet the world quietly")

	rec := awaitRelease(t, d, issue)
	if rec.Result != "held" || !strings.Contains(rec.Reason, "too low to judge") {
		t.Fatalf("the release without metrics ended %+v", rec)
	}
	candidate := d.candidateDigest(t, issue)
	for name, digest := range d.deployed(t, "app", true) {
		if digest == candidate {
			t.Fatalf("the held candidate was deployed as %s", name)
		}
	}
	// A held release is not retried by itself: the issue stays a candidate
	// and the edge keeps serving the last known good release.
	time.Sleep(10 * time.Second)
	if r := recordOf(t, d, issue); r.State != "ReleaseCandidate" {
		t.Fatalf("the held issue moved to %s", r.State)
	}
	if got := servedRevision(t, d); got != before {
		t.Fatalf("the edge serves %s after a held release, want %s", got, before)
	}
}

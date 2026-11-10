//go:build e2e

package e2e

import "testing"

// An issue goes the whole way through the Docker install: the scripted
// agent's patch is built into a candidate, judged by the independent fake
// judge with a signed receipt, released by canary at 5, 25 and 100 percent
// through the edge under real load, observed, verified, published and
// closed. The supervisor runs every revision through its deploy filter, and
// keeps nothing of the application but the release and the way back to the
// revision it replaced.
func TestComposeReleasesAnIssueTheWholeWay(t *testing.T) {
	d := newComposeRelease(t)
	before := servedRevision(t, d)
	runFullLifecycle(t, d)
	released := servedRevision(t, d)
	running := d.deployed(t, "app", true)
	found := false
	for name, digest := range running {
		switch digest {
		case released:
			found = true
		case before:
		default:
			t.Fatalf("after the release %s still holds %s; only the release and the revision it replaced are kept", name, digest)
		}
	}
	if !found {
		t.Fatalf("after the release no container runs %s: %v", released, running)
	}
}

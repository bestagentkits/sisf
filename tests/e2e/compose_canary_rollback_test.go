//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
)

// A revision that dies during its canary is rolled back to the last known
// good release by itself: the edge serves the LKG again, the issue ends
// Blocked, and the recovery report is on the issue.
func TestComposeRollsABadCanaryBackAndBlocksTheIssue(t *testing.T) {
	d := newComposeRelease(t)
	seedRelease(t, d, greetingDiff)
	before := servedRevision(t, d)
	load := startTraffic(t, d)
	number, issue := openBug(t, d, "Greet the world, badly")

	waitStateWithin(t, d, issue, "Canary", releaseLimit)
	candidate := d.candidateDigest(t, issue)
	var canary string
	eventually(t, "the canary container", func() (bool, string) {
		for name, digest := range d.deployed(t, "app", false) {
			if digest == candidate {
				canary = name
			}
		}
		return canary != "", "none labelled " + candidate
	})
	// The bad revision: its process is gone while it takes a share of the
	// traffic.
	if out, err := d.docker(t, "stop", "-t", "1", canary); err != nil {
		t.Fatalf("stop the canary: %v\n%s", err, out)
	}

	waitStateWithin(t, d, issue, "Blocked", releaseLimit)
	load.halt()
	journeyEnds(t, d, issue, []string{"Canary", "RolledBack", "Blocked"})
	if rec := awaitRelease(t, d, issue); rec.Result != "blocked" {
		t.Fatalf("the release ended %+v", rec)
	}
	if got := servedRevision(t, d); got != before {
		t.Fatalf("after the rollback the edge serves %s, want the last known good %s", got, before)
	}
	gh := d.github()
	// The factory posts the recovery report after it records the rollback.
	eventually(t, "the recovery report on the issue", func() (bool, string) {
		n := gh.commentsWith(t, number, "Release rolled back")
		return n == 1, fmt.Sprintf("%d reports", n)
	})
	for name, digest := range d.deployed(t, "app", false) {
		if digest != before || strings.Contains(name, candidate[len("sha256:"):][:12]) {
			t.Fatalf("after the rollback %s runs %s", name, digest)
		}
	}
}

//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// releaseAgainAfterTheOwnerMovesTheBaseline opens a second issue of the unit
// the factory just released and merged. The policy in force still lacks that
// release, so the candidate is refused after QA and the application keeps
// serving the first release. The Owner then puts a commit that contains the
// merged release in force and retries the same issue, which is released the
// whole way by a new run.
func releaseAgainAfterTheOwnerMovesTheBaseline(t *testing.T, d *standaloneDeployment) {
	t.Helper()
	serving := servedRevision(t, d)
	setPatch(t, d, greetingDiff)
	number, issue := openBug(t, d, "Greet the world again")

	waitStateWithin(t, d, issue, "Blocked", releaseLimit)
	refused := awaitRelease(t, d, issue)
	if refused.Result != "refused" || !strings.HasPrefix(refused.Reason, "APPROVAL_REQUIRED") ||
		!strings.Contains(refused.Reason, "the Owner moves the trust baseline") {
		t.Fatalf("the second release of the unit ended %+v, want it refused for the Owner to move the trust baseline", refused)
	}
	if got := servedRevision(t, d); got != serving {
		t.Fatalf("the application serves %s after a refused release, it served %s", got, serving)
	}
	firstRun := recordOf(t, d, issue).RunID

	seedPolicy(t, d)
	if out, err := d.factoryd("control", "issue", "retry", "--issue", issue); err != nil {
		t.Fatalf("the Owner's retry of %s: %v\n%s", issue, err, out)
	}
	load := startTraffic(t, d)
	waitStateWithin(t, d, issue, "Closed", releaseLimit)
	load.halt()
	if run := recordOf(t, d, issue).RunID; run == firstRun {
		t.Fatalf("the retry of %s was released by run %s, the run that was refused", issue, run)
	}
	if released := awaitRelease(t, d, issue); released.Result != "closed" {
		t.Fatalf("the retried release ended %+v", released)
	}
	candidate := d.candidateDigest(t, issue)
	if got := servedRevision(t, d); got != candidate || got == serving {
		t.Fatalf("the application serves %s after the retried release of %s (it served %s before)", got, candidate, serving)
	}
	if sent, failed := load.sent.Load(), load.failed.Load(); sent == 0 || failed != 0 {
		t.Fatalf("the application failed %d of %d requests during the retried release", failed, sent)
	}
	gh := d.github()
	eventually(t, "GitHub to show the retried issue closed", func() (bool, string) {
		state := gh.issueState(t, number)
		return state == "closed", "it is " + state
	})
}

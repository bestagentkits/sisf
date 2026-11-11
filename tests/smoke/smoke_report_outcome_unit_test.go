//go:build smoke

package smoke

import (
	"strings"
	"testing"
)

// The reported outcome follows the checks as well as the test: a report
// that lists a failed check never says PASS.
func TestTheReportedOutcomeFollowsTheChecks(t *testing.T) {
	t.Parallel()
	passed := []checkResult{{Name: "issue closed", Status: statusPass}, {Name: "note", Status: statusInfo}}
	if got := reportOutcome(false, "teardown", passed); got != "PASS" {
		t.Errorf("every check passed: %q, want PASS", got)
	}
	failed := append(passed, checkResult{Name: "release record pull request closed", Status: statusFail})
	if got := reportOutcome(false, "teardown", failed); !strings.HasPrefix(got, "FAIL") || !strings.Contains(got, "release record pull request closed") {
		t.Errorf("a failed check: %q, want FAIL naming it", got)
	}
	if got := reportOutcome(true, "the release", passed); got != "FAIL during the release" {
		t.Errorf("a failed test: %q", got)
	}
}

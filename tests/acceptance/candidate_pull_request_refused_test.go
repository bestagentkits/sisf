//go:build unix

package acceptance

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/pkg/core"
)

// When GitHub refuses to open the candidate's pull request, as for a token
// without Pull requests: write, the candidate is left for the Owner with
// no pull request, and the issue still closes on its verified release,
// saying the change is not merged.
func TestAPullRequestGitHubRefusesToOpenLeavesTheCandidateForTheOwner(t *testing.T) {
	t.Parallel()
	f, number := greetingIssue(t)
	f.GitData.FailOn(http.MethodPost, "/pulls", http.StatusForbidden, "Resource not accessible by integration")
	f.Cycle(3)
	wantSurfaces(t, f, number, core.StateClosed)
	pr := candidatePR(t, f, number)
	if pr.State != handoff.LandingLeftOpen || pr.Number != 0 || !strings.Contains(pr.Reason, "status 403") || f.GitData.Merges() != 0 || mainHolds(f) {
		t.Fatalf("the candidate: %+v after %d merges", pr, f.GitData.Merges())
	}
	progress := commentContaining(t, f, number, "<!-- sisf:progress ")
	if !strings.Contains(progress, "**Not merged:** the candidate is left for the Owner") || !strings.Contains(progress, "the change is **not** merged") {
		t.Errorf("the progress comment reads:\n%s", progress)
	}
	if seen := f.Inspect(number); seen.PullRequest == nil || seen.PullRequest.Number != 0 || seen.PullRequest.State != string(handoff.LandingLeftOpen) {
		t.Errorf("runs.inspect shows %+v", seen.PullRequest)
	}
	if why := f.Explain(number); !hasEvidence(why, "candidate_pr") || !strings.Contains(why.Reason, "the candidate is left for the Owner") {
		t.Errorf("runs.explain answers %+v", why)
	}
}

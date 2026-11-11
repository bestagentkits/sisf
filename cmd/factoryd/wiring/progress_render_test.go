package wiring

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The progress comment shows every checkpoint of a run in order, from the
// claim to the close, and says at the close whether the change is merged.
// It is built from numbers, states, gate names and fixed reasons only: an
// attempt's failure detail never reaches the issue.
func TestTheProgressCommentShowsEveryCheckpointAndWhetherTheChangeIsMerged(t *testing.T) {
	t.Parallel()
	issue := core.IssueRef{Owner: "acme", Repo: "app", Number: 7}
	facts := progressFacts{
		record: core.StateRecord{IssueID: issue, State: core.StateClosed, RunID: "run-1", Attempt: 2},
		run: &dispatcher.RunRecord{RunID: "run-1", Failures: []dispatcher.AttemptFailure{
			{Attempt: 1, Code: core.CodeUnavailable, Detail: "token ghp_secret in the agent's output"}}},
		notes: []handoff.QANote{
			{Attempt: 1, MaxAttempts: 3, Verdict: "failed", Built: true, Receipt: true, Score: 100, PassScore: 85, FailedGates: []string{"observed_build_confidence"}},
			{Attempt: 2, MaxAttempts: 3, Verdict: "passed", Built: true, Receipt: true, Score: 92, PassScore: 85},
		},
		pr: &handoff.CandidatePR{Number: 12, URL: "https://github.com/acme/app/pull/12", Base: "main", State: handoff.LandingLeftOpen,
			Reason: "the branch moved after QA judged the candidate"},
	}
	body := renderProgress(facts)
	order := []string{
		"**Now:** `Closed`", "Claimed by run `run-1`", "Coding attempt 1 failed (`UNAVAILABLE`)",
		"QA attempt 1 of 3: failed, score 100 against the pass mark 85; failed hard gates: `observed_build_confidence`",
		"QA attempt 2 of 3: passed, score 92", "Candidate pull request: #12", "Release: ReleaseCandidate → Canary → Monitoring → Verified",
		"**Not merged:** #12 is left for the Owner: the branch moved", "Closed:", "the change is **not** merged",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(body[at:], want)
		if i < 0 {
			t.Fatalf("the progress lacks %q after position %d:\n%s", want, at, body)
		}
		at += i
	}
	if strings.Contains(body, "ghp_secret") {
		t.Fatalf("an attempt's detail reached the issue:\n%s", body)
	}
	facts.pr.State, facts.pr.MergeCommit = handoff.LandingMerged, "abc"
	if body := renderProgress(facts); !strings.Contains(body, "**Merged** #12 into `main` as `abc`") || !strings.Contains(body, "the change is merged") {
		t.Fatalf("a merged change reads:\n%s", body)
	}
}

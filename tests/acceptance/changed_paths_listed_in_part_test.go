//go:build unix

package acceptance

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
)

// When GitHub's comparison of a candidate lists as many files as GitHub
// lists at most, the list may be cut short. The QA stage marks the
// candidate as listed in part, and the supply-chain gate, which cannot
// judge a list that may be incomplete, fails it: the candidate is not
// released, merged or given a pull request.
func TestACandidateGitHubListsOnlyInPartFailsQA(t *testing.T) {
	t.Parallel()
	f, number := greetingIssue(t)
	f.GitData.CompareBeyondTheLimit()
	f.Cycle(2)
	var result stages.QAResult
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		if result, err = stages.ReadQAResult(ctx, tx, f.Record(number).RunID, 1); err != nil {
			return err
		}
		if _, err := handoff.ReadCandidatePR(ctx, tx, f.Record(number).RunID); !core.HasCode(err, core.CodeNotFound) {
			t.Errorf("the candidate has a pull request record: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("QA judged no candidate: %v", err)
	}
	if !result.Candidate.ChangedPathsIncomplete {
		t.Errorf("the candidate is not marked as listed in part (%d paths kept)", len(result.Candidate.ChangedPaths))
	}
	if result.Verdict != stages.VerdictFailed || !strings.Contains(result.Reason, string(qa.GateSupplyChain)) {
		t.Errorf("QA verdict %s: %s", result.Verdict, result.Reason)
	}
	if state := f.Record(number).State; state == core.StateReleaseCandidate || state == core.StateClosed || f.GitData.Merges() != 0 || mainHolds(f) {
		t.Fatalf("the candidate went on: issue in %s after %d merges", state, f.GitData.Merges())
	}
}

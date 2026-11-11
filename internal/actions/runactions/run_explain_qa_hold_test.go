package runactions_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
)

// holds are the QA holds the supervisor recorded, by run and attempt.
type holds map[core.RunID]runactions.QAHold

func (h holds) QAHold(_ context.Context, run core.RunID, attempt int) (runactions.QAHold, error) {
	hold, ok := h[run]
	if !ok || attempt != 1 {
		return runactions.QAHold{}, core.NewError(core.CodeNotFound, "no hold")
	}
	return hold, nil
}

// An issue whose candidate QA held sits in QAEvaluation with no step out,
// and runs.explain says it is stopped, why, and with the hold as evidence:
// the last attempt failing QA is a run out of attempts, and a pass the
// Owner must approve awaits approval. Without a hold the issue is in
// progress.
func TestExplainReportsAHeldEvaluationAsStopped(t *testing.T) {
	t.Parallel()
	w := world{
		records: records{
			issueRef(1): record(1, core.StateQAEvaluation, "run-1", 1),
			issueRef(2): record(2, core.StateQAEvaluation, "run-2", 2),
			issueRef(3): record(3, core.StateQAEvaluation, "run-3", 3),
		},
		runs: runs{
			"run-1": {Issue: issueRef(1), RunID: "run-1", Status: dispatcher.RunSucceeded, Attempt: 3},
			"run-2": {Issue: issueRef(2), RunID: "run-2", Status: dispatcher.RunSucceeded, Attempt: 1},
			"run-3": {Issue: issueRef(3), RunID: "run-3", Status: dispatcher.RunSucceeded, Attempt: 1},
		},
		holds: holds{
			"run-1": {Ref: "qa-hold/run-1/1", Summary: "QA failed attempt 3 of 3 of run run-1: hard gate observed_build_confidence failed (score 100, pass mark 85).", At: t0},
			"run-2": {Ref: "qa-hold/run-2/1", AwaitsApproval: true, Summary: "QA passed attempt 1 of run run-2 with score 90 on the coder's own model route.", At: t0},
		},
	}
	explain := w.action(t, runactions.ActionRunsExplain)
	for _, c := range []struct {
		issue   string
		stopped bool
		cause   runactions.Cause
		reason  string
	}{
		{`{"issue":"acme/app#1"}`, true, runactions.CauseAttemptsExhausted, "observed_build_confidence"},
		{`{"issue":"acme/app#2"}`, true, runactions.CauseAwaitingApproval, "coder's own model route"},
		{`{"issue":"acme/app#3"}`, false, runactions.CauseInProgress, "QAEvaluation"},
	} {
		got, err := call[runactions.Explanation](t, explain, c.issue)
		if err != nil {
			t.Fatal(err)
		}
		held := slices.Contains(evidenceKinds(got), "qa_hold")
		if got.Stopped != c.stopped || got.Cause != c.cause || !strings.Contains(got.Reason, c.reason) || held != c.stopped {
			t.Errorf("%s: %+v", c.issue, got)
		}
	}
}

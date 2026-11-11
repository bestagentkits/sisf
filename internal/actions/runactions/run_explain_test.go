package runactions_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

func evidenceKinds(e runactions.Explanation) []string {
	var kinds []string
	for _, item := range e.Evidence {
		kinds = append(kinds, item.Kind)
	}
	return kinds
}

func nextNames(e runactions.Explanation) []string {
	var names []string
	for _, n := range e.NextActions {
		names = append(names, n.Action)
	}
	return names
}

func TestExplainSaysWhyARunIsStoppedWithEvidence(t *testing.T) {
	t.Parallel()
	rejected := approvals.Request{ID: "scope-1", IssueID: issueRef(1), Action: approvals.ActionApproveScope,
		RiskSummary: "Adds a new feature.", Status: approvals.StatusRejected, CreatedAt: t0}
	pending := approvals.Request{ID: "scope-2", IssueID: issueRef(2), Action: approvals.ActionApproveScope,
		RiskSummary: "Adds billing.", Status: approvals.StatusPending, CreatedAt: t0}
	w := world{
		records: records{
			issueRef(1): record(1, core.StateBlocked, "", 1),
			issueRef(2): record(2, core.StateApprovalPending, "", 2),
			issueRef(3): record(3, core.StateDeveloping, "run-3", 3),
			issueRef(4): record(4, core.StateClosed, "run-4", 4),
			issueRef(5): record(5, core.StateClarifying, "", 5),
			issueRef(6): record(6, core.StateRolledBack, "run-6", 6),
			issueRef(7): record(7, core.StateDeveloping, "run-7", 7),
		},
		runs: runs{
			"run-3": {Issue: issueRef(3), RunID: "run-3", Status: dispatcher.RunExhausted, Attempt: 3,
				Failures: []dispatcher.AttemptFailure{{Attempt: 3, Code: core.CodeTimeout, Detail: "agent timed out", At: t0}}},
			"run-7": {Issue: issueRef(7), RunID: "run-7", Status: dispatcher.RunWorking, Attempt: 1},
		},
		inbox: inbox{requests: []approvals.Request{rejected, pending}, decisions: map[string]approvals.Approval{
			"scope-1": {Decision: approvals.DecisionReject, ActorID: "u-owner", Reason: "out of scope", DecidedAt: t0}}},
	}
	explain := w.action(t, runactions.ActionRunsExplain)
	cases := []struct {
		in       string
		stopped  bool
		cause    runactions.Cause
		evidence []string
		next     string
		mention  string
	}{
		{`{"issue":"acme/app#1"}`, true, runactions.CauseBlocked, []string{"state_record", "approval_request", "approval_decision"}, "approvals.list", "out of scope"},
		{`{"issue":"acme/app#2"}`, true, runactions.CauseAwaitingApproval, []string{"state_record", "approval_request"}, "approvals.get", "Adds billing."},
		{`{"run_id":"run-3"}`, true, runactions.CauseAttemptsExhausted, []string{"state_record", "run_record", "attempt_failure"}, "runs.inspect", "agent timed out"},
		{`{"issue":"acme/app#4"}`, false, runactions.CauseFinished, []string{"state_record"}, "", ""},
		{`{"issue":"acme/app#5"}`, true, runactions.CauseAwaitingInformation, []string{"state_record"}, "runs.inspect", ""},
		{`{"issue":"acme/app#6"}`, true, runactions.CauseRolledBack, []string{"state_record"}, "runs.inspect", ""},
		{`{"issue":"acme/app#7"}`, false, runactions.CauseInProgress, []string{"state_record", "run_record"}, "runs.inspect", ""},
	}
	for _, tc := range cases {
		got, err := call[runactions.Explanation](t, explain, tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if got.Stopped != tc.stopped || got.Cause != tc.cause || got.Reason == "" {
			t.Errorf("%s: stopped=%v cause=%s reason=%q", tc.in, got.Stopped, got.Cause, got.Reason)
		}
		if kinds := evidenceKinds(got); !slices.Equal(kinds, tc.evidence) {
			t.Errorf("%s: evidence = %v, want %v", tc.in, kinds, tc.evidence)
		}
		if tc.next != "" && !slices.Contains(nextNames(got), tc.next) {
			t.Errorf("%s: next actions %v lack %s", tc.in, nextNames(got), tc.next)
		}
		var all strings.Builder
		for _, e := range got.Evidence {
			all.WriteString(e.Summary + "\n")
		}
		if tc.cause == runactions.CauseAttemptsExhausted && !strings.Contains(got.Reason, string(core.CodeTimeout)) {
			t.Errorf("%s: the reason does not name the code of the last failure: %q", tc.in, got.Reason)
		}
		if !strings.Contains(all.String(), tc.mention) {
			t.Errorf("%s: evidence %q does not mention %q", tc.in, all.String(), tc.mention)
		}
	}
}

func TestExplainRefusesBadTargets(t *testing.T) {
	t.Parallel()
	w := world{records: records{issueRef(1): record(1, core.StateDeveloping, "run-new", 1)},
		runs: runs{"run-old": {Issue: issueRef(1), RunID: "run-old"}}}
	explain := w.action(t, runactions.ActionRunsExplain)
	cases := map[string]core.ErrorCode{
		`{}`:                                  core.CodeInvalidArgument,
		`{"issue":"acme/app#1","run_id":"x"}`: core.CodeInvalidArgument,
		`{"issue":"not a ref"}`:               core.CodeInvalidArgument,
		`{"issue":"acme/app#9"}`:              core.CodeNotFound,
		`{"run_id":"run-missing"}`:            core.CodeNotFound,
		`{"run_id":"run-old"}`:                core.CodeConflict,
		`{"issue":"acme/app#1","x":1}`:        core.CodeInvalidArgument,
	}
	for in, want := range cases {
		if _, err := call[runactions.Explanation](t, explain, in); !core.HasCode(err, want) {
			t.Errorf("%s: error = %v, want %s", in, err, want)
		}
	}
}

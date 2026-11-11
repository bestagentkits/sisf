package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Every QA verdict is logged with its reason, and a held one as a warning
// that says the issue waits for a person: a run must never sit in
// QAEvaluation without a line saying why.
func TestTheSupervisorLogsEveryQAVerdictWithItsReason(t *testing.T) {
	t.Parallel()
	passed, failed, held := core.IssueRef{Owner: "a", Repo: "b", Number: 1}, core.IssueRef{Owner: "a", Repo: "b", Number: 2}, core.IssueRef{Owner: "a", Repo: "b", Number: 3}
	var out bytes.Buffer
	logPass(slog.New(slog.NewTextHandler(&out, nil)), stages.PassResult{QA: stages.QAPassResult{
		Passed: []core.IssueRef{passed}, Failed: []core.IssueRef{failed}, Held: []core.IssueRef{held},
		Reasons: map[core.IssueRef]string{failed: `hard gate "secrets" failed`, held: `attempt 3 of 3 failed QA: hard gate "observed_build_confidence" failed`},
	}})
	log := out.String()
	for _, want := range []string{
		`level=INFO msg="candidate passed QA" issue=a/b#1`,
		`level=INFO msg="candidate failed QA; the run's next attempt follows" issue=a/b#2 reason="hard gate \"secrets\" failed"`,
		`level=WARN msg="candidate held after QA: the issue waits in QAEvaluation for the Owner" issue=a/b#3 reason="attempt 3 of 3 failed QA`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %s:\n%s", want, log)
		}
	}
}

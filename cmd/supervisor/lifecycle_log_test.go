package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
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

// Every release the stages ran is logged once, when it ends: a closed one
// as information, and one that was held, refused, undone, interrupted or
// could not be restored as a warning with its result, the state it left
// the issue in, the cause of a block, its code and its reason. A held
// release is not tried again by itself, so it is logged once, and again
// only when the Owner's retry starts it over.
func TestTheSupervisorLogsEveryReleaseWithItsResultCodeAndReason(t *testing.T) {
	t.Parallel()
	issue := func(n int) core.IssueRef { return core.IssueRef{Owner: "a", Repo: "b", Number: n} }
	var out bytes.Buffer
	logPass(slog.New(slog.NewTextHandler(&out, nil)), stages.PassResult{Release: stages.ReleasePassResult{Outcomes: map[core.IssueRef]release.Outcome{
		issue(1): {Result: release.ResultClosed, State: core.StateClosed, Release: "sha256:aa"},
		issue(2): {Result: release.ResultHeld, State: core.StateReleaseCandidate, Release: "sha256:bb",
			Reason: "RELEASE_HOLD: real traffic is too low to judge the release and the application has no synthetic suite"},
		issue(3): {Result: release.ResultBlocked, State: core.StateBlocked, Release: "sha256:cc", Reason: "critical health", Cause: "rolled_back"},
		issue(4): {Result: release.ResultRefused, State: core.StateBlocked, Release: "sha256:dd", Reason: "EXPIRED: the receipt expired"},
		issue(5): {Result: release.ResultRefused, State: core.StateReleaseCandidate, Release: "sha256:ee", Reason: "EXPIRED: the receipt expired"},
		issue(6): {Result: release.ResultInterrupted, State: core.StateCanary, Release: "sha256:ff", Reason: "context canceled"},
	}}})
	log := out.String()
	for _, want := range []string{
		`level=INFO msg="release closed: the verified release serves the application" issue=a/b#1 release=sha256:aa`,
		`level=WARN msg="release held before anything was deployed: the issue waits in ReleaseCandidate for the Owner's retry" issue=a/b#2 result=held state=ReleaseCandidate cause="" code=RELEASE_HOLD reason="RELEASE_HOLD: real traffic is too low`,
		`level=WARN msg="release undone: the last known good release serves again and the issue is blocked until the Owner's retry returns it to Ready" issue=a/b#3 result=blocked state=Blocked cause=rolled_back`,
		`level=WARN msg="release refused before anything was deployed: the issue is blocked and the Owner's retry returns it to Ready for a new run" issue=a/b#4 result=refused state=Blocked cause="" code=EXPIRED`,
		`level=WARN msg="release refused before anything was deployed, and the issue could not be blocked yet: the next pass tries again" issue=a/b#5 result=refused state=ReleaseCandidate`,
		`level=WARN msg="release stopped after its rollout started: the issue is left where the state says, and the supervisor's recovery restores the last known good release and blocks it" issue=a/b#6 result=interrupted state=Canary`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %s:\n%s", want, log)
		}
	}
}

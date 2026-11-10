package statemachine

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// checkAttemptsWith runs the step from QAEvaluation back to Developing after
// mutate has changed the request, the proof, or both.
func checkAttemptsWith(mutate func(*core.Transition, *AttemptsProof)) error {
	request := qaRetryRequest()
	proof := validAttempts(request)
	mutate(&request, &proof)
	guards := Guards{Attempts: attemptsWith(func(core.Transition) AttemptsProof { return proof })}
	return machineWith(guards).Check(context.Background(), request)
}

// attemptsUsed sets how many attempts the run has made and has the request
// start the next one.
func attemptsUsed(used int) func(*core.Transition, *AttemptsProof) {
	return func(r *core.Transition, p *AttemptsProof) {
		p.Attempt = used
		r.Attempt = used + 1
	}
}

func TestAFailedCandidateIsRetriedWhileAttemptsRemain(t *testing.T) {
	t.Parallel()
	if err := checkAttemptsWith(attemptsUsed(1)); err != nil {
		t.Fatalf("the second of three attempts was refused: %v", err)
	}
	if err := checkAttemptsWith(attemptsUsed(testMaxAttempts - 1)); err != nil {
		t.Fatalf("the last attempt the limit allows was refused: %v", err)
	}
}

func TestARunThatReachedItsMaxAttemptsIsNotRetried(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*core.Transition, *AttemptsProof){
		"the run used its last attempt":     attemptsUsed(testMaxAttempts),
		"the run is already past its limit": attemptsUsed(testMaxAttempts + 4),
		"a limit of one allows no retry": func(r *core.Transition, p *AttemptsProof) {
			p.Attempt, p.MaxAttempts, r.Attempt = 1, 1, 2
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkAttemptsWith(mutate), GuardAttempts)
	}
}

func TestARetryWithoutAStatedLimitIsRefused(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*core.Transition, *AttemptsProof){
		"no limit was stated":           func(_ *core.Transition, p *AttemptsProof) { p.MaxAttempts = 0 },
		"the limit is negative":         func(_ *core.Transition, p *AttemptsProof) { p.MaxAttempts = -1 },
		"no attempt was counted":        func(r *core.Transition, p *AttemptsProof) { p.Attempt, r.Attempt = 0, 1 },
		"the count is negative":         func(r *core.Transition, p *AttemptsProof) { p.Attempt, r.Attempt = -3, 0 },
		"the count is of another run":   func(_ *core.Transition, p *AttemptsProof) { p.RunID = otherRun },
		"the count is of another issue": func(_ *core.Transition, p *AttemptsProof) { p.IssueID = otherIssue },
		"the proof names no run":        func(_ *core.Transition, p *AttemptsProof) { p.RunID = "" },
		"the retry names no run": func(r *core.Transition, p *AttemptsProof) {
			r.RunID, p.RunID = "", ""
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkAttemptsWith(mutate), GuardAttempts)
	}
}

func TestARetryMustCountAsTheNextAttempt(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*core.Transition, *AttemptsProof){
		"the attempt number does not advance": func(r *core.Transition, p *AttemptsProof) { r.Attempt = p.Attempt },
		"the attempt number is reset to one":  func(r *core.Transition, p *AttemptsProof) { p.Attempt, r.Attempt = 2, 1 },
		"the attempt number is reset to zero": func(r *core.Transition, _ *AttemptsProof) { r.Attempt = 0 },
		"the attempt number skips ahead":      func(r *core.Transition, p *AttemptsProof) { r.Attempt = p.Attempt + 2 },
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkAttemptsWith(mutate), GuardAttempts)
	}
}

func TestAnEmptyAttemptsProofIsRefused(t *testing.T) {
	t.Parallel()
	guards := Guards{Attempts: attemptsWith(func(core.Transition) AttemptsProof { return AttemptsProof{} })}
	err := machineWith(guards).Check(context.Background(), qaRetryRequest())
	requireGuardFailed(t, "empty proof", err, GuardAttempts)
}

func TestAFailingCandidateCannotLoopPastItsMaxAttempts(t *testing.T) {
	t.Parallel()
	// The record's attempt count is what the guard reports; every accepted
	// retry advances it, as the store does when it writes the step.
	recorded := 1
	guards := Guards{Attempts: AttemptsGuardFunc(func(_ context.Context, t core.Transition) (AttemptsProof, error) {
		return AttemptsProof{IssueID: t.IssueID, RunID: t.RunID, Attempt: recorded, MaxAttempts: testMaxAttempts}, nil
	})}
	machine := machineWith(guards)

	retries := 0
	for range 1000 {
		request := qaRetryRequest()
		request.Attempt = recorded + 1
		if machine.Check(context.Background(), request) != nil {
			break
		}
		recorded = request.Attempt
		retries++
	}
	if retries != testMaxAttempts-1 || recorded != testMaxAttempts {
		t.Fatalf("a failing candidate was retried %d times and reached attempt %d, want %d retries and attempt %d",
			retries, recorded, testMaxAttempts-1, testMaxAttempts)
	}

	// Neither replaying the last attempt number nor starting the count over
	// reopens the loop.
	for _, attempt := range []int{0, 1, testMaxAttempts, testMaxAttempts + 1} {
		request := qaRetryRequest()
		request.Attempt = attempt
		requireGuardFailed(t, "retry after the limit", machine.Check(context.Background(), request), GuardAttempts)
	}
}

func TestARefusedRetryReportsTheAttemptsUsedAndTheLimit(t *testing.T) {
	t.Parallel()
	details := detailsOf(checkAttemptsWith(attemptsUsed(testMaxAttempts)))
	if details["attempt"] != "3" || details["max_attempts"] != "3" {
		t.Fatalf("refusal details = %v, want the attempts used and the limit", details)
	}
}

func TestTheLastAttemptMayStillPassEvaluation(t *testing.T) {
	t.Parallel()
	// Running out of retries closes the way back to Developing only; a
	// candidate that passes on its final attempt is still released.
	guards := passingGuards()
	guards.Attempts = attemptsWith(func(t core.Transition) AttemptsProof {
		return AttemptsProof{IssueID: t.IssueID, RunID: t.RunID, Attempt: testMaxAttempts, MaxAttempts: testMaxAttempts}
	})
	if err := machineWith(guards).Check(context.Background(), qaPassRequest()); err != nil {
		t.Fatalf("a passing final attempt was refused: %v", err)
	}
}

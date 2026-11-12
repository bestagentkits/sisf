package statemachine

import (
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// A receipt releases only the candidate it was issued for, and only once the
// instant it claims to have been issued at has come.

func TestAReceiptForAnotherIssueRunOrCandidateDoesNotReleaseThisOne(t *testing.T) {
	t.Parallel()
	proofs := map[string]func(*QAProof){
		"receipt is for another issue":     func(p *QAProof) { p.Receipt.IssueID = otherIssue },
		"receipt is for another run":       func(p *QAProof) { p.Receipt.RunID = otherRun },
		"receipt is for another candidate": func(p *QAProof) { p.Receipt.CandidateDigest = core.Digest(otherBuild) },
	}
	for name, mutate := range proofs {
		requireGuardFailed(t, name, checkQAWith(mutate), GuardQAPass)
	}
	requests := map[string]func(*core.Transition){
		"the step is for another issue":     func(r *core.Transition) { r.IssueID = otherIssue },
		"the step is for another run":       func(r *core.Transition) { r.RunID = otherRun },
		"the step is for another candidate": func(r *core.Transition) { r.CandidateDigest = core.Digest(otherBuild) },
		"the step names no run":             func(r *core.Transition) { r.RunID = "" },
		"the step names no candidate":       func(r *core.Transition) { r.CandidateDigest = "" },
	}
	for name, mutate := range requests {
		requireGuardFailed(t, name, checkQAOn(mutate, intact), GuardQAPass)
	}
}

func TestAReceiptIssuedInTheFutureDoesNotReleaseACandidate(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*QAProof){
		"issued a moment after now": func(p *QAProof) { p.Receipt.IssuedAt = testNow.Add(time.Nanosecond) },
		"issued an hour from now": func(p *QAProof) {
			p.Receipt.IssuedAt = testNow.Add(time.Hour)
			p.Receipt.ExpiresAt = testNow.Add(2 * time.Hour)
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkQAWith(mutate), GuardQAPass)
	}
	if err := checkQAWith(func(p *QAProof) { p.Receipt.IssuedAt = testNow }); err != nil {
		t.Fatalf("a receipt issued at this very instant was refused: %v", err)
	}
}

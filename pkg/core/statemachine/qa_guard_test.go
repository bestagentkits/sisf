package statemachine

import (
	"context"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// checkQAWith runs the step into ReleaseCandidate after mutate has changed
// the proof.
func checkQAWith(mutate func(*QAProof)) error {
	return checkQAOn(func(*core.Transition) {}, mutate)
}

// checkQAOn runs the step into ReleaseCandidate after the request and the
// proof have each been changed.
func checkQAOn(mutateRequest func(*core.Transition), mutate func(*QAProof)) error {
	request := qaPassRequest()
	mutateRequest(&request)
	proof := validQA()
	mutate(&proof)
	return machineWith(Guards{QA: qaWith(proof)}).Check(context.Background(), request)
}

func intact(*QAProof) {}

func TestACandidateIsReleasedOnAValidReceiptWithEveryHardGatePassing(t *testing.T) {
	if err := checkQAWith(func(*QAProof) {}); err != nil {
		t.Fatalf("a valid passing receipt was refused: %v", err)
	}
}

func TestAScoreEqualToThePassScorePasses(t *testing.T) {
	if err := checkQAWith(func(p *QAProof) { scored(p, p.PassScore) }); err != nil {
		t.Fatalf("a score exactly at the pass score was refused: %v", err)
	}
}

func TestAnInvalidReceiptDoesNotReleaseACandidate(t *testing.T) {
	cases := map[string]func(*QAProof){
		"no receipt was presented": func(p *QAProof) { p.Receipt = core.QAReceipt{} },
		"receipt is unsigned":      func(p *QAProof) { p.Receipt.Signature = core.Signature{} },
		"receipt names no built candidate": func(p *QAProof) {
			p.Receipt.CandidateDigest = ""
		},
		"candidate is a branch, not a commit": func(p *QAProof) {
			p.Receipt.CandidateSHA = "feature-branch"
		},
		"evaluator scored its own candidate": func(p *QAProof) {
			p.Receipt.QAAgentIdentity = p.Receipt.CodingAgentIdentity
		},
		"shared model route is not disclosed": func(p *QAProof) {
			p.Receipt.QAModelRoute = p.Receipt.CodingModelRoute
		},
		"receipt expired a moment ago": func(p *QAProof) {
			p.Receipt.ExpiresAt = testNow.Add(-1)
		},
		"receipt expires at this very instant": func(p *QAProof) {
			p.Receipt.ExpiresAt = testNow
		},
		"receipt names no issue": func(p *QAProof) { p.Receipt.IssueID = core.IssueRef{} },
		"receipt names no run":   func(p *QAProof) { p.Receipt.RunID = "" },
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkQAWith(mutate), GuardQAPass)
	}
}

func TestAFailedOrMissingHardGateBlocksTheRelease(t *testing.T) {
	cases := map[string]func(*QAProof){
		"a hard gate failed": func(p *QAProof) { gateCheck(p, "rollback_readiness").Passed = false },
		"a perfect score does not override a failed hard gate": func(p *QAProof) {
			gateCheck(p, "tests_contract").Passed = false
			scored(p, 100)
		},
		"no hard gate was recorded": func(p *QAProof) {
			p.Receipt.Checks = slices.DeleteFunc(p.Receipt.Checks, func(c core.QACheck) bool {
				return c.Kind == core.CheckHardGate
			})
			p.RequiredGates = nil
		},
		"a required hard gate was never run": func(p *QAProof) {
			p.RequiredGates = append(p.RequiredGates, "license_scan")
		},
		"a required gate was recorded only as a scored category": func(p *QAProof) {
			gateCheck(p, "rollback_readiness").Kind = core.CheckRubric
		},
		"a gate that is not required failed": func(p *QAProof) {
			p.Receipt.Checks = append(p.Receipt.Checks,
				core.QACheck{Name: "license_scan", Kind: core.CheckHardGate, Passed: false})
		},
		"a required gate was recorded twice and failed once": func(p *QAProof) {
			p.Receipt.Checks = append(p.Receipt.Checks,
				core.QACheck{Name: "tests_contract", Kind: core.CheckHardGate, Passed: false})
		},
		"policy names no required hard gates": func(p *QAProof) { p.RequiredGates = nil },
		"policy names an empty hard gate": func(p *QAProof) {
			p.RequiredGates = append(p.RequiredGates, "")
		},
		"policy names only an empty hard gate": func(p *QAProof) { p.RequiredGates = []string{""} },
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkQAWith(mutate), GuardQAPass)
	}
}

func TestAScoreBelowThePassScoreBlocksTheRelease(t *testing.T) {
	cases := map[string]func(*QAProof){
		"score one point short":         func(p *QAProof) { scored(p, p.PassScore-1) },
		"score of zero":                 func(p *QAProof) { scored(p, 0) },
		"policy states no pass score":   func(p *QAProof) { p.PassScore = 0 },
		"pass score is negative":        func(p *QAProof) { p.PassScore = -5 },
		"pass score is above the scale": func(p *QAProof) { p.PassScore = 101 },
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkQAWith(mutate), GuardQAPass)
	}
}

func TestAScoreThatIsNotTheSumOfTheRubricPointsBlocksTheRelease(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*QAProof){
		"score claims more than the points recorded": func(p *QAProof) { p.Receipt.Score = 100 },
		"score claims one point more":                func(p *QAProof) { p.Receipt.Score++ },
		"score claims less than the points recorded": func(p *QAProof) { p.Receipt.Score = p.PassScore },
		"a passing score with points far below it": func(p *QAProof) {
			for i := range p.Receipt.Checks {
				if p.Receipt.Checks[i].Kind == core.CheckRubric {
					p.Receipt.Checks[i].Points = 1
				}
			}
		},
		"a score with no rubric category behind it": func(p *QAProof) {
			p.Receipt.Checks = slices.DeleteFunc(p.Receipt.Checks, func(c core.QACheck) bool {
				return c.Kind == core.CheckRubric
			})
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkQAWith(mutate), GuardQAPass)
	}
}

func TestAnEmptyQAProofIsRefused(t *testing.T) {
	err := machineWith(Guards{QA: qaWith(QAProof{})}).Check(context.Background(), qaPassRequest())
	requireGuardFailed(t, "empty proof", err, GuardQAPass)
}

func TestAFailedEvaluationStillReturnsToDevelopingWithoutAReceipt(t *testing.T) {
	// Only the step into ReleaseCandidate needs a receipt; sending the
	// candidate back for another attempt must not be held up by one. It is
	// held to the attempt limit alone.
	guards := Guards{Attempts: attemptsWith(validAttempts)}
	if err := machineWith(guards).Check(context.Background(), qaRetryRequest()); err != nil {
		t.Fatalf("returning a failed candidate to Developing was refused: %v", err)
	}
}

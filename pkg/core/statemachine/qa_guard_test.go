package statemachine

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// checkQAWith runs the step into ReleaseCandidate after mutate has changed
// the proof.
func checkQAWith(mutate func(*QAProof)) error {
	proof := validQA()
	mutate(&proof)
	return machineWith(Guards{QA: qaWith(proof)}).Check(context.Background(), qaPassRequest())
}

func TestACandidateIsReleasedOnAValidReceiptWithEveryHardGatePassing(t *testing.T) {
	if err := checkQAWith(func(*QAProof) {}); err != nil {
		t.Fatalf("a valid passing receipt was refused: %v", err)
	}
}

func TestAScoreEqualToThePassScorePasses(t *testing.T) {
	if err := checkQAWith(func(p *QAProof) { p.Receipt.Score = p.PassScore }); err != nil {
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
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkQAWith(mutate), GuardQAPass)
	}
}

func TestAFailedOrMissingHardGateBlocksTheRelease(t *testing.T) {
	cases := map[string]func(*QAProof){
		"a hard gate failed": func(p *QAProof) { p.Receipt.Checks[1].Passed = false },
		"a perfect score does not override a failed hard gate": func(p *QAProof) {
			p.Receipt.Checks[0].Passed = false
			p.Receipt.Score = 100
		},
		"no hard gate was recorded": func(p *QAProof) {
			p.Receipt.Checks = p.Receipt.Checks[2:]
			p.RequiredGates = nil
		},
		"a required hard gate was never run": func(p *QAProof) {
			p.RequiredGates = append(p.RequiredGates, "secrets")
		},
		"a required gate was recorded only as a scored category": func(p *QAProof) {
			p.Receipt.Checks[1].Kind = core.CheckRubric
		},
		"a gate that is not required failed": func(p *QAProof) {
			p.Receipt.Checks = append(p.Receipt.Checks,
				core.QACheck{Name: "migrations", Kind: core.CheckHardGate, Passed: false})
		},
		"a required gate was recorded twice and failed once": func(p *QAProof) {
			p.Receipt.Checks = append(p.Receipt.Checks,
				core.QACheck{Name: "tests_contract", Kind: core.CheckHardGate, Passed: false})
		},
		"policy names no required hard gates": func(p *QAProof) { p.RequiredGates = nil },
		"policy names an empty hard gate":     func(p *QAProof) { p.RequiredGates = []string{""} },
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkQAWith(mutate), GuardQAPass)
	}
}

func TestAScoreBelowThePassScoreBlocksTheRelease(t *testing.T) {
	cases := map[string]func(*QAProof){
		"score one point short":         func(p *QAProof) { p.Receipt.Score = p.PassScore - 1 },
		"score of zero":                 func(p *QAProof) { p.Receipt.Score = 0 },
		"policy states no pass score":   func(p *QAProof) { p.PassScore = 0 },
		"pass score is negative":        func(p *QAProof) { p.PassScore = -5 },
		"pass score is above the scale": func(p *QAProof) { p.PassScore = 101 },
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
	// candidate back for another attempt must not be held up by one.
	request := transitionOn(core.StateQAEvaluation, core.EventQAFailed, core.StateDeveloping)
	if err := machineWith(Guards{}).Check(context.Background(), request); err != nil {
		t.Fatalf("returning a failed candidate to Developing was refused: %v", err)
	}
}

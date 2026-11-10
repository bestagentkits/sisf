package statemachine

import (
	"context"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// The review criteria name the gates and the pass score a receipt is judged
// by. They may raise the bar; the tests below show they cannot lower it, and
// that the release step and the evaluator's rubric apply one pass rule.

func TestTheReviewCriteriaCannotRequireFewerGatesThanTheRequiredSet(t *testing.T) {
	t.Parallel()
	for _, gate := range core.RequiredHardGates() {
		without := func(gates []string) []string {
			return slices.DeleteFunc(slices.Clone(gates), func(name string) bool { return name == gate })
		}
		dropped := checkQAWith(func(p *QAProof) { p.RequiredGates = without(p.RequiredGates) })
		requireGuardFailed(t, "criteria leave out "+gate, dropped, GuardQAPass)
		if named := detailsOf(dropped)["gate"]; named != gate {
			t.Errorf("criteria leave out %s: the refusal names gate %q", gate, named)
		}

		// A receipt that skipped the gate must not pass because the criteria
		// presented with it skip the same gate.
		skipped := checkQAWith(func(p *QAProof) {
			p.RequiredGates = without(p.RequiredGates)
			p.Receipt.Checks = slices.DeleteFunc(p.Receipt.Checks, func(c core.QACheck) bool {
				return c.Kind == core.CheckHardGate && c.Name == gate
			})
		})
		requireGuardFailed(t, "criteria and receipt both leave out "+gate, skipped, GuardQAPass)

		unrun := checkQAWith(func(p *QAProof) {
			p.Receipt.Checks = slices.DeleteFunc(p.Receipt.Checks, func(c core.QACheck) bool {
				return c.Kind == core.CheckHardGate && c.Name == gate
			})
		})
		requireGuardFailed(t, "receipt leaves out "+gate, unrun, GuardQAPass)
	}
	twoGates := checkQAWith(func(p *QAProof) { p.RequiredGates = []string{"tests_contract", "rollback_readiness"} })
	requireGuardFailed(t, "criteria name two of the nine gates", twoGates, GuardQAPass)
}

func TestAnExtraGateOfTheReviewCriteriaIsEnforced(t *testing.T) {
	t.Parallel()
	err := checkQAWith(func(p *QAProof) {
		p.RequiredGates = append(p.RequiredGates, "license_scan")
		p.Receipt.Checks = append(p.Receipt.Checks,
			core.QACheck{Name: "license_scan", Kind: core.CheckHardGate, Passed: true})
	})
	if err != nil {
		t.Fatalf("a receipt that passed the extra gate of the criteria was refused: %v", err)
	}
}

func TestTheReleaseStepAndTheRubricGiveOneVerdictOnEveryReceipt(t *testing.T) {
	t.Parallel()
	rubric := qa.DefaultRubric()
	cases := map[string]func(*QAProof){
		"passing":                   intact,
		"score at the pass score":   func(p *QAProof) { scored(p, rubric.PassScore) },
		"score one point short":     func(p *QAProof) { scored(p, rubric.PassScore-1) },
		"a required gate failed":    func(p *QAProof) { gateCheck(p, "secrets").Passed = false },
		"a required gate never ran": func(p *QAProof) { gateCheck(p, "supply_chain").Kind = core.CheckRubric },
		"an unlisted gate failed": func(p *QAProof) {
			p.Receipt.Checks = append(p.Receipt.Checks, core.QACheck{Name: "license_scan", Kind: core.CheckHardGate})
		},
		"score above the points": func(p *QAProof) { p.Receipt.Score = 100 },
		"only two of the gates ran": func(p *QAProof) {
			p.Receipt.Checks = slices.DeleteFunc(p.Receipt.Checks, func(c core.QACheck) bool {
				return c.Kind == core.CheckHardGate && c.Name != "tests_contract" && c.Name != "rollback_readiness"
			})
		},
	}
	for name, mutate := range cases {
		proof := validQA()
		proof.PassScore = rubric.PassScore
		mutate(&proof)
		released := machineWith(Guards{QA: qaWith(proof)}).Check(context.Background(), qaPassRequest()) == nil
		if passes := rubric.Passes(proof.Receipt); passes != released {
			t.Errorf("%s: the rubric says pass=%v but the release step says %v", name, passes, released)
		}
	}
}

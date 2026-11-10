package core

import (
	"fmt"
	"slices"
)

// requiredHardGates is the closed set of deterministic gates every candidate
// must pass. Review criteria may add gates; nothing may drop one.
var requiredHardGates = []string{
	"tests_contract",
	"permissions",
	"migrations",
	"secrets",
	"observed_build_confidence",
	"independent_evaluator",
	"resource_budget_policy",
	"rollback_readiness",
	"supply_chain",
}

// RequiredHardGates returns the names of the hard gates every candidate must
// pass. It is the single list both the evaluator and the release step judge
// against. The caller owns the returned slice.
func RequiredHardGates() []string { return slices.Clone(requiredHardGates) }

// RubricPoints returns the points the scored rubric categories of the receipt
// add up to. Points recorded on a hard gate are not part of the score.
func (r QAReceipt) RubricPoints() int {
	total := 0
	for _, c := range r.Checks {
		if c.Kind == CheckRubric {
			total += c.Points
		}
	}
	return total
}

// CheckPass applies the pass rule, the one definition of "QA passed" shared
// by the evaluator and the step into ReleaseCandidate. It returns nil only
// when all of the following hold, and GUARD_FAILED otherwise:
//
//   - passScore is within 1-100;
//   - no hard gate failed, and every gate of RequiredHardGates and every gate
//     in extraGates has a passing hard-gate result;
//   - the score equals the sum of the rubric points;
//   - the score reaches passScore.
//
// The gates are judged before the score, so a high score never buys past a
// failed gate. extraGates are gates the review criteria demand on top of the
// required set; they can add to it and never replace it. When a gate is the
// reason, the details of the refusal name it under "gate".
//
// CheckPass decides the verdict only. Whether the receipt is complete,
// authentic, fresh and bound to the right candidate is checked separately.
func (r QAReceipt) CheckPass(passScore int, extraGates ...string) error {
	if passScore < 1 || passScore > 100 {
		return notPassed("the review criteria state no pass score within 1-100")
	}
	passed := map[string]bool{}
	for _, c := range r.Checks {
		if c.Kind != CheckHardGate {
			continue
		}
		if !c.Passed {
			return gateNotPassed(c.Name, "hard gate %q failed", c.Name)
		}
		passed[c.Name] = true
	}
	for _, gate := range slices.Concat(requiredHardGates, extraGates) {
		if gate == "" || !passed[gate] {
			return gateNotPassed(gate, "required hard gate %q has no passing result", gate)
		}
	}
	if points := r.RubricPoints(); r.Score != points {
		return notPassed("QA score %d does not equal the %d rubric points recorded", r.Score, points)
	}
	if r.Score < passScore {
		return notPassed("QA score %d is below the pass score %d", r.Score, passScore)
	}
	return nil
}

// notPassed builds the refusal of the pass rule.
func notPassed(format string, args ...any) *Error {
	return NewError(CodeGuardFailed, fmt.Sprintf(format, args...))
}

// gateNotPassed builds a refusal that names the hard gate behind it.
func gateNotPassed(gate, format string, args ...any) *Error {
	refusal := notPassed(format, args...)
	refusal.Details = map[string]string{"gate": gate}
	return refusal
}

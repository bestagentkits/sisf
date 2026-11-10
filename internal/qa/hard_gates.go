package qa

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// runGates runs every hard gate, in the order of qa.Gates, and records each
// result. The independent-evaluator gate is the evaluator's own: by the time
// gates run, the coder's identity, workspace and route have been checked. A
// gate that cannot run, or reports for another gate, is recorded as failed:
// a gate that could not look passes nothing.
func (e *Evaluator) runGates(ctx context.Context, c qa.Candidate, route routeChoice) []core.QACheck {
	var checks []core.QACheck
	for _, name := range qa.Gates() {
		if name == qa.GateIndependentEvaluator {
			checks = append(checks, e.independenceResult(c, route).Check())
			continue
		}
		checks = append(checks, runGate(ctx, e.gates[name], c).Check())
	}
	return checks
}

// runGate runs one gate and turns an error or a misreport into a failure.
func runGate(ctx context.Context, gate qa.HardGate, c qa.Candidate) qa.GateResult {
	name := gate.Name()
	result, err := gate.Check(ctx, c)
	switch {
	case err != nil:
		return qa.GateResult{Gate: name, Detail: sprintf("the gate could not run (%s)", core.CodeOf(err))}
	case result.Gate != name:
		return qa.GateResult{Gate: name, Detail: sprintf("the gate reported a result for %q", result.Gate)}
	}
	return result
}

// independenceResult records who judged on which route against whom.
func (e *Evaluator) independenceResult(c qa.Candidate, route routeChoice) qa.GateResult {
	detail := sprintf("evaluator %s on %s judged the work of %s on %s",
		e.cfg.Identity, route.route, c.CodingAgentIdentity, c.CodingModelRoute)
	if route.same {
		detail += "; the deployment has one model route, so QA shares the coder's"
	}
	return qa.GateResult{Gate: qa.GateIndependentEvaluator, Passed: true, Detail: detail}
}

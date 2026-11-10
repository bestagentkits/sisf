// Package qa defines the contract of independent evaluation: the scoring
// rubric, the hard gates and the evaluator that issues a signed receipt.
//
// The evaluator is never the agent that wrote the candidate, works from a
// read-only baseline pinned to a commit and a policy, and scores only code
// that has finished building. A score cannot override a failed hard gate, and
// a candidate cannot change the criteria it is judged by.
package qa

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Category is one scored area of the rubric.
type Category string

const (
	// CategoryCorrectness is correctness and regression.
	CategoryCorrectness Category = "correctness_regression"
	// CategorySecurity is security and authentication.
	CategorySecurity Category = "security_auth"
	// CategoryContract is contract and data safety.
	CategoryContract Category = "contract_data_safety"
	// CategoryExperience is UX, performance and output.
	CategoryExperience Category = "ux_performance_output"
	// CategoryObservability is observability and rollback.
	CategoryObservability Category = "observability_rollback"
)

// Categories returns the five rubric categories.
func Categories() []Category {
	return []Category{CategoryCorrectness, CategorySecurity, CategoryContract, CategoryExperience, CategoryObservability}
}

// Rubric holds the weights and the pass score. The values in force come from
// the review criteria at an Owner-approved commit; an agent cannot lower them.
type Rubric struct {
	Weights   map[Category]int `json:"weights"`
	PassScore int              `json:"pass_score"`
}

// DefaultRubric returns the starting rubric: 30/25/20/15/10 with a pass score
// of 85. These are calibration starting points, not proven thresholds.
func DefaultRubric() Rubric {
	return Rubric{
		Weights: map[Category]int{
			CategoryCorrectness:   30,
			CategorySecurity:      25,
			CategoryContract:      20,
			CategoryExperience:    15,
			CategoryObservability: 10,
		},
		PassScore: 85,
	}
}

// Validate requires exactly the five categories, non-negative weights that
// sum to 100 and a pass score within 1-100.
func (r Rubric) Validate() error {
	total := 0
	for _, c := range Categories() {
		w, ok := r.Weights[c]
		if !ok || w < 0 {
			return core.NewError(core.CodeInvalidArgument, "rubric needs a non-negative weight for "+string(c))
		}
		total += w
	}
	switch {
	case len(r.Weights) != len(Categories()):
		return core.NewError(core.CodeInvalidArgument, "rubric contains an unknown category")
	case total != 100:
		return core.NewError(core.CodeInvalidArgument, "rubric weights must sum to 100")
	case r.PassScore < 1 || r.PassScore > 100:
		return core.NewError(core.CodeInvalidArgument, "rubric pass score must be within 1-100")
	}
	return nil
}

// Passes reports whether a receipt meets the rubric: every hard gate was run
// and passed, and the score reaches the pass score. The gates are checked
// first and independently of the score.
func (r Rubric) Passes(receipt core.QAReceipt) bool {
	return len(MissingGates(receipt.Checks)) == 0 && receipt.HardGatesPassed() && receipt.Score >= r.PassScore
}

// GateName names a hard gate.
type GateName string

const (
	GateTestsContract GateName = "tests_contract"
	GatePermissions   GateName = "permissions"
	GateMigrations    GateName = "migrations"
	GateSecrets       GateName = "secrets"
	// GateObservedBuild is confidence that the build under test is the one observed.
	GateObservedBuild GateName = "observed_build_confidence"
	// GateIndependentEvaluator is that the evaluator differs from the coder.
	GateIndependentEvaluator GateName = "independent_evaluator"
	GateResourceBudget       GateName = "resource_budget_policy"
	GateRollbackReadiness    GateName = "rollback_readiness"
	// GateSupplyChain covers changes to dependencies, lockfiles, base images
	// and CI configuration.
	GateSupplyChain GateName = "supply_chain"
)

// Gates returns every hard gate. Each must be run for every candidate.
func Gates() []GateName {
	return []GateName{GateTestsContract, GatePermissions, GateMigrations, GateSecrets, GateObservedBuild,
		GateIndependentEvaluator, GateResourceBudget, GateRollbackReadiness, GateSupplyChain}
}

// MissingGates returns the hard gates that have no hard-gate result in
// checks. A gate that was never run is treated as not passed.
func MissingGates(checks []core.QACheck) []GateName {
	recorded := map[string]bool{}
	for _, c := range checks {
		if c.Kind == core.CheckHardGate {
			recorded[c.Name] = true
		}
	}
	var missing []GateName
	for _, g := range Gates() {
		if !recorded[string(g)] {
			missing = append(missing, g)
		}
	}
	return missing
}

// GateResult is the outcome of one hard gate.
type GateResult struct {
	Gate        GateName `json:"gate"`
	Passed      bool     `json:"passed"`
	Detail      string   `json:"detail,omitempty"`
	EvidenceRef string   `json:"evidence_ref,omitempty"`
}

// Check converts the result to the form recorded in a receipt.
func (g GateResult) Check() core.QACheck {
	return core.QACheck{Name: string(g.Gate), Kind: core.CheckHardGate, Passed: g.Passed, Detail: g.Detail, EvidenceRef: g.EvidenceRef}
}

// Candidate is a built change submitted for evaluation.
type Candidate struct {
	IssueID   core.IssueRef  `json:"issue_id"`
	RunID     core.RunID     `json:"run_id"`
	CommitSHA core.CommitSHA `json:"commit_sha"`
	// Digest is the digest of the finished build. A candidate without one
	// has not finished building and cannot be scored.
	Digest      core.Digest    `json:"digest"`
	BaselineSHA core.CommitSHA `json:"baseline_sha"`
	SuiteDigest core.Digest    `json:"suite_digest"`
	// Policy pins the criteria in force and the model route QA runs on.
	Policy              core.PolicyRefs `json:"policy"`
	CodingAgentIdentity string          `json:"coding_agent_identity"`
	CodingModelRoute    core.ModelRoute `json:"coding_model_route"`
	ChangedPaths        []string        `json:"changed_paths,omitempty"`
	// WorkspaceID names the read-only checkout the evaluator inspects.
	WorkspaceID string `json:"workspace_id,omitempty"`
	// Attempt is the evaluator attempt, starting at 1.
	Attempt int `json:"attempt"`
}

// Validate rejects a candidate that is unbuilt, unpinned or anonymous.
func (c Candidate) Validate() error {
	checks := []func() error{
		c.IssueID.Validate, c.RunID.Validate, c.CommitSHA.Validate, c.Digest.Validate,
		c.BaselineSHA.Validate, c.SuiteDigest.Validate, c.Policy.Validate, c.CodingModelRoute.Validate,
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	if c.CodingAgentIdentity == "" || c.Attempt < 1 {
		return core.NewError(core.CodeInvalidArgument, "candidate must name its coding agent and carry an attempt of at least 1")
	}
	return nil
}

// HardGate is one deterministic check with veto power. An error means the
// gate could not be run, which blocks the candidate just as a failure does.
type HardGate interface {
	Name() GateName
	Check(ctx context.Context, c Candidate) (GateResult, error)
}

// Evaluator judges a candidate and returns a signed receipt bound to the
// candidate's commit and digest. It refuses a candidate written by its own
// identity and one that has not finished building.
type Evaluator interface {
	Evaluate(ctx context.Context, c Candidate) (core.QAReceipt, error)
}

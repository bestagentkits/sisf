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
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Category is one scored area of the rubric.
type Category string

// The categories are those of core.RubricCategories, the list the release
// step judges a receipt against.
const (
	// CategoryCorrectness is correctness and regression.
	CategoryCorrectness Category = core.RubricCorrectness
	// CategorySecurity is security and authentication.
	CategorySecurity Category = core.RubricSecurity
	// CategoryContract is contract and data safety.
	CategoryContract Category = core.RubricContract
	// CategoryExperience is UX, performance and output.
	CategoryExperience Category = core.RubricExperience
	// CategoryObservability is observability and rollback.
	CategoryObservability Category = core.RubricObservability
)

// Categories returns the five rubric categories, in the order of
// core.RubricCategories.
func Categories() []Category {
	defined := core.RubricCategories()
	categories := make([]Category, len(defined))
	for i, category := range defined {
		categories[i] = Category(category.Name)
	}
	return categories
}

// Rubric holds the weights and the pass score. The values in force come from
// the review criteria at an Owner-approved commit; an agent cannot lower them.
type Rubric struct {
	Weights   map[Category]int `json:"weights"`
	PassScore int              `json:"pass_score"`
}

// DefaultRubric returns the starting rubric: the weights of
// core.RubricCategories, 30/25/20/15/10, with a pass score of 85. These are
// calibration starting points, not proven thresholds.
func DefaultRubric() Rubric {
	weights := map[Category]int{}
	for _, category := range core.RubricCategories() {
		weights[Category(category.Name)] = category.MaxPoints
	}
	return Rubric{Weights: weights, PassScore: 85}
}

// Validate requires exactly the five categories, each at the weight
// core.RubricCategories gives it, and a pass score within 1-100. The weights
// are those the release step scores a receipt out of, so a rubric with other
// weights could never agree with it; only the pass score varies.
func (r Rubric) Validate() error {
	defined := core.RubricCategories()
	for _, category := range defined {
		weight, ok := r.Weights[Category(category.Name)]
		if !ok || weight != category.MaxPoints {
			return core.NewError(core.CodeInvalidArgument,
				fmt.Sprintf("rubric must weigh %s at %d", category.Name, category.MaxPoints))
		}
	}
	switch {
	case len(r.Weights) != len(defined):
		return core.NewError(core.CodeInvalidArgument, "rubric contains an unknown category")
	case r.PassScore < 1 || r.PassScore > 100:
		return core.NewError(core.CodeInvalidArgument, "rubric pass score must be within 1-100")
	}
	return nil
}

// Passes reports whether a receipt meets the rubric: every hard gate was run
// and passed, every category is scored once out of its weight, the score
// equals the rubric points recorded and it reaches the pass score. A rubric
// that is not valid passes nothing. The verdict is core.QAReceipt.CheckPass,
// the same rule the step into ReleaseCandidate applies, so the evaluator and
// the release step cannot disagree about what passing means.
func (r Rubric) Passes(receipt core.QAReceipt) bool {
	return r.Validate() == nil && receipt.CheckPass(r.PassScore) == nil
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

// Gates returns every hard gate. Each must be run for every candidate. The
// set is core.RequiredHardGates, the list the release step requires, so a
// gate cannot be demanded in one place and forgotten in the other.
func Gates() []GateName {
	names := core.RequiredHardGates()
	gates := make([]GateName, len(names))
	for i, name := range names {
		gates[i] = GateName(name)
	}
	return gates
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

// Evaluator judges a candidate and returns a receipt bound to its own factory
// and to the candidate's issue, run, commit and digest, signed over
// core.QAReceipt.SigningPayload. It scores each category of
// core.RubricCategories once, out of the category's weight, and the score it
// records is the sum of those points. It refuses a candidate written by its
// own identity, compared with core.SameIdentity, and one that has not
// finished building.
type Evaluator interface {
	Evaluate(ctx context.Context, c Candidate) (core.QAReceipt, error)
}

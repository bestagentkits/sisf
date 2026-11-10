package core

import (
	"slices"
	"strings"
)

// ModelRoute is the model a step runs on. It is chosen by code from
// configuration and recorded with the run; a model is never asked to pick it.
type ModelRoute struct {
	Provider string `json:"provider" yaml:"provider"`
	Model    string `json:"model" yaml:"model"`
	Effort   string `json:"effort,omitempty" yaml:"effort,omitempty"`
}

// IsZero reports whether the route is unset.
func (m ModelRoute) IsZero() bool { return m == ModelRoute{} }

// String renders "provider/model" or "provider/model@effort".
func (m ModelRoute) String() string {
	if m.Effort == "" {
		return m.Provider + "/" + m.Model
	}
	return m.Provider + "/" + m.Model + "@" + m.Effort
}

// SameModel reports whether both routes use the same provider and model.
// Effort is ignored: more effort on the same model shares its blind spots.
// Letter case and surrounding white space are ignored too, so a shared route
// cannot be passed off as a second one by respelling it.
func (m ModelRoute) SameModel(other ModelRoute) bool {
	return sameName(m.Provider, other.Provider) && sameName(m.Model, other.Model)
}

// sameName compares two provider or model names without regard to letter case
// or surrounding white space.
func sameName(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// Validate requires a provider and a model.
func (m ModelRoute) Validate() error {
	if strings.TrimSpace(m.Provider) == "" || strings.TrimSpace(m.Model) == "" {
		return invalid("model route %q needs a provider and a model", m.String())
	}
	return nil
}

// PolicyRefs pins the policy a decision was made under. Storing the refs with
// every decision lets an auditor replay it, and lets the factory hold when a
// foundation file is not at an Owner-approved commit.
type PolicyRefs struct {
	// PolicySHA is the commit of the approved factory configuration and policy.
	PolicySHA CommitSHA `json:"policy_sha"`
	// ReviewSHA is the commit of the QA and release criteria.
	ReviewSHA CommitSHA `json:"review_sha"`
	// DecisionSHA is the commit of the decision rules and typed questions.
	DecisionSHA CommitSHA `json:"decision_sha"`
	// FeatureCatalogSHA is the commit of the approved feature catalogue.
	FeatureCatalogSHA CommitSHA  `json:"feature_catalog_sha"`
	ModelRoute        ModelRoute `json:"model_route"`
}

// Validate requires every reference to be a full commit and the route to be set.
func (p PolicyRefs) Validate() error {
	for _, sha := range []CommitSHA{p.PolicySHA, p.ReviewSHA, p.DecisionSHA, p.FeatureCatalogSHA} {
		if err := sha.Validate(); err != nil {
			return err
		}
	}
	return p.ModelRoute.Validate()
}

// StepKind is the kind of work a step does. Model routes are configured per
// kind, so a step can change tier without a code change.
type StepKind string

const (
	// StepIntake covers classification, intake, labelling and extraction.
	StepIntake StepKind = "intake"
	// StepCoding covers coding against a clear scope of work.
	StepCoding StepKind = "coding"
	// StepPlanning covers migrations, large refactors and architecture planning.
	StepPlanning StepKind = "planning"
	// StepReadOnly covers read-only helpers: finding files, reading logs, summarizing.
	StepReadOnly StepKind = "read_only"
	// StepQA covers independent evaluation of a candidate.
	StepQA StepKind = "qa"
)

var allStepKinds = []StepKind{StepIntake, StepCoding, StepPlanning, StepReadOnly, StepQA}

// StepKinds returns every step kind. The caller owns the returned slice.
func StepKinds() []StepKind { return slices.Clone(allStepKinds) }

// Valid reports whether k is a known step kind.
func (k StepKind) Valid() bool { return slices.Contains(allStepKinds, k) }

// DelegationMode is how much a deployment lets agents do without a person.
// No mode lets an agent publish upstream or widen its own authority.
type DelegationMode string

const (
	// DelegationOnDemand (A): an agent runs only when a person starts it.
	DelegationOnDemand DelegationMode = "A"
	// DelegationProposal (B): an agent proposes; a person grants permission
	// before anything is modified or released.
	DelegationProposal DelegationMode = "B"
	// DelegationBounded (C): changes that pass every deterministic gate need
	// no per-change technical approval.
	DelegationBounded DelegationMode = "C"
)

// Valid reports whether m is A, B or C.
func (m DelegationMode) Valid() bool {
	return m == DelegationOnDemand || m == DelegationProposal || m == DelegationBounded
}

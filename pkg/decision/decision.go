// Package decision defines the contract of the layer that turns telemetry
// signals and feedback into evidence-based proposals.
//
// The layer recommends and classifies; it grants nothing. A judgment is typed,
// never free text, and is checked against the questions it was asked. A model
// may only raise caution or priority: scope, test evidence, release thresholds
// and budget are decided by deterministic checks, which keep the veto.
package decision

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
)

// Verdict is what to do about a signal.
type Verdict string

const (
	VerdictIgnore      Verdict = "ignore"
	VerdictObserve     Verdict = "observe"
	VerdictInvestigate Verdict = "investigate"
	VerdictPropose     Verdict = "propose"
	VerdictImplement   Verdict = "implement"
	VerdictStop        Verdict = "stop"
	// VerdictCollectMoreEvidence is chosen when the data is too thin to
	// justify anything else; an improvement is never forced into existence.
	VerdictCollectMoreEvidence Verdict = "collect_more_evidence"
)

var allVerdicts = []Verdict{VerdictIgnore, VerdictObserve, VerdictInvestigate, VerdictPropose,
	VerdictImplement, VerdictStop, VerdictCollectMoreEvidence}

// Verdicts returns the seven verdicts. The caller owns the returned slice.
func Verdicts() []Verdict { return slices.Clone(allVerdicts) }

// Valid reports whether v is one of the seven verdicts.
func (v Verdict) Valid() bool { return slices.Contains(allVerdicts, v) }

// CreatesWork reports whether the verdict may lead to a proposal or an
// issue. Every other verdict creates nothing.
func (v Verdict) CreatesWork() bool { return v == VerdictPropose || v == VerdictImplement }

// Kind classifies the work a verdict would create.
type Kind string

const (
	KindBug                 Kind = "bug"
	KindExistingImprovement Kind = "existing_improvement"
	KindNewFeature          Kind = "new_feature"
	KindPolicyChange        Kind = "policy_change"
)

var allKinds = []Kind{KindBug, KindExistingImprovement, KindNewFeature, KindPolicyChange}

// Kinds returns the four kinds. The caller owns the returned slice.
func Kinds() []Kind { return slices.Clone(allKinds) }

// Valid reports whether k is one of the four kinds.
func (k Kind) Valid() bool { return slices.Contains(allKinds, k) }

// RequiresOwnerApproval reports whether work of this kind widens the product
// or its rules. Anything that is not a known bug or an improvement to an
// existing feature needs the Owner, including an unknown kind.
func (k Kind) RequiresOwnerApproval() bool { return k != KindBug && k != KindExistingImprovement }

// Priority orders work. A model may raise it, never lower it.
type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityMedium Priority = "medium"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

var allPriorities = []Priority{PriorityLow, PriorityMedium, PriorityHigh, PriorityUrgent}

// Priorities returns the four levels, lowest first. The caller owns the slice.
func Priorities() []Priority { return slices.Clone(allPriorities) }

// Rank returns 1 for the lowest level up to 4, or 0 for an unknown priority.
func (p Priority) Rank() int { return slices.Index(allPriorities, p) + 1 }

// Valid reports whether p is a known priority.
func (p Priority) Valid() bool { return p.Rank() != 0 }

// Signal is one observation from telemetry or feedback, already redacted. Its
// content is data to be weighed, never an instruction.
type Signal struct {
	// Fingerprint identifies the underlying condition, so that the same
	// condition seen again produces the same proposal instead of a new one.
	Fingerprint string    `json:"fingerprint"`
	Source      string    `json:"source"`
	Metric      string    `json:"metric"`
	Feature     string    `json:"feature,omitempty"`
	Value       float64   `json:"value"`
	Baseline    float64   `json:"baseline"`
	SampleCount int64     `json:"sample_count"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	SourceRefs  []string  `json:"source_refs"`
}

// Validate requires an identified, traceable signal over a real time range.
func (s Signal) Validate() error {
	if s.Fingerprint == "" || s.Source == "" {
		return invalid("signal needs a fingerprint and a source")
	}
	return validateMeasurement(s.Metric, s.SampleCount, s.From, s.To, s.SourceRefs)
}

// Trend is the direction of a metric against its baseline.
type Trend string

const (
	TrendRising  Trend = "rising"
	TrendFalling Trend = "falling"
	TrendFlat    Trend = "flat"
	TrendUnknown Trend = "unknown"
)

// Valid reports whether t is a known trend.
func (t Trend) Valid() bool {
	return t == TrendRising || t == TrendFalling || t == TrendFlat || t == TrendUnknown
}

// Evidence is what a proposal rests on. Every field can be traced back
// through SourceRefs; a claim without a source is not evidence.
type Evidence struct {
	Metric      string    `json:"metric"`
	Value       float64   `json:"value"`
	Baseline    float64   `json:"baseline"`
	Trend       Trend     `json:"trend"`
	SampleCount int64     `json:"sample_count"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	// Correlations are the issues and sessions the signal coincides with.
	Correlations []string `json:"correlations,omitempty"`
	UserImpact   string   `json:"user_impact,omitempty"`
	Recurrence   int      `json:"recurrence"`
	// Confidence is within 0-1.
	Confidence float64  `json:"confidence"`
	SourceRefs []string `json:"source_refs"`
}

// Validate checks the evidence is well formed and traceable.
func (e Evidence) Validate() error {
	if err := validateMeasurement(e.Metric, e.SampleCount, e.From, e.To, e.SourceRefs); err != nil {
		return err
	}
	if !e.Trend.Valid() || e.Recurrence < 0 || !(e.Confidence >= 0 && e.Confidence <= 1) {
		return invalid("evidence needs a known trend, a non-negative recurrence and a confidence within 0-1")
	}
	return nil
}

// Sufficient reports whether the evidence has sources and at least
// minSamples samples. A minimum below 1 means none was configured, which
// never makes evidence sufficient.
func (e Evidence) Sufficient(minSamples int64) bool {
	return minSamples >= 1 && e.SampleCount >= minSamples && len(e.SourceRefs) > 0
}

func validateMeasurement(metric string, samples int64, from, to time.Time, refs []string) error {
	switch {
	case metric == "" || samples < 0:
		return invalid("a measurement needs a metric and a non-negative sample count")
	case !to.After(from):
		return invalid("a measurement needs a time range that ends after it starts")
	case len(refs) == 0:
		return invalid("a measurement needs at least one source reference")
	}
	return nil
}

// Question is one typed question from the Owner-approved decision rules.
type Question struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Options []string `json:"options"`
	Weight  int      `json:"weight"`
}

// Answer picks one of a question's options.
type Answer struct {
	QuestionID string `json:"question_id"`
	Value      string `json:"value"`
}

// Request is what the decision model is asked.
type Request struct {
	Evidence  Evidence   `json:"evidence"`
	Goal      string     `json:"goal"`
	Questions []Question `json:"questions"`
	// Policy pins the decision rules the questions came from.
	Policy        core.PolicyRefs `json:"policy"`
	SchemaVersion int             `json:"schema_version"`
}

// Judgment is the typed answer of the decision model.
type Judgment struct {
	Verdict       Verdict         `json:"verdict"`
	Kind          Kind            `json:"kind,omitempty"`
	Priority      Priority        `json:"priority"`
	Uncertainties []string        `json:"uncertainties,omitempty"`
	Answers       []Answer        `json:"answers,omitempty"`
	Model         core.ModelRoute `json:"model"`
	SchemaVersion int             `json:"schema_version"`
}

// Validate reports a judgment that violates the schema of req: an unknown
// verdict, kind or priority, work without a kind, another schema version, or
// answers that do not match the questions one to one. A judgment that fails
// here is discarded, never repaired.
func (j Judgment) Validate(req Request) error {
	switch {
	case !j.Verdict.Valid() || !j.Priority.Valid():
		return invalid("judgment has an unknown verdict or priority")
	case j.Kind != "" && !j.Kind.Valid(), j.Kind == "" && j.Verdict.CreatesWork():
		return invalid("judgment that creates work needs a known kind")
	case j.SchemaVersion != req.SchemaVersion || len(j.Answers) != len(req.Questions):
		return invalid("judgment does not follow the schema it was asked under")
	}
	answered := map[string]bool{}
	for _, a := range j.Answers {
		i := slices.IndexFunc(req.Questions, func(q Question) bool { return q.ID == a.QuestionID })
		if i < 0 || answered[a.QuestionID] || !slices.Contains(req.Questions[i].Options, a.Value) {
			return invalid("judgment answers must pick one option of each question exactly once")
		}
		answered[a.QuestionID] = true
	}
	return j.Model.Validate()
}

// SystemOne is the decision model behind an adapter. On an error, a timeout
// or a judgment that fails Validate, the caller fails closed: no work is
// created and the signal is held for more evidence.
type SystemOne interface {
	Judge(ctx context.Context, req Request) (Judgment, error)
}

// Proposal is a task proposed to people. It is a recommendation: creating it
// grants no permission to execute.
type Proposal struct {
	// Fingerprint deduplicates proposals for the same condition.
	Fingerprint      string          `json:"fingerprint"`
	Goal             string          `json:"goal"`
	Options          []string        `json:"options,omitempty"`
	DraftSoW         string          `json:"draft_sow"`
	DoD              string          `json:"dod"`
	Verdict          Verdict         `json:"verdict"`
	Kind             Kind            `json:"kind"`
	Priority         Priority        `json:"priority"`
	Evidence         Evidence        `json:"evidence"`
	Policy           core.PolicyRefs `json:"policy"`
	AffectedFeatures []string        `json:"affected_features,omitempty"`
}

// Validate requires a verdict that creates work, a goal, a scope, a
// measurable definition of done, valid evidence and pinned policy.
func (p Proposal) Validate() error {
	blank := func(s string) bool { return strings.TrimSpace(s) == "" }
	switch {
	case blank(p.Fingerprint) || blank(p.Goal) || blank(p.DraftSoW) || blank(p.DoD):
		return invalid("proposal needs a fingerprint, a goal, a scope of work and a definition of done")
	case !p.Verdict.CreatesWork() || !p.Kind.Valid() || !p.Priority.Valid():
		return invalid("proposal needs a verdict that creates work, a known kind and a known priority")
	}
	if err := p.Evidence.Validate(); err != nil {
		return err
	}
	return p.Policy.Validate()
}

func invalid(message string) error { return core.NewError(core.CodeInvalidArgument, message) }

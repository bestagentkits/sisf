package decision

import (
	"github.com/bestagentkits/sisf/pkg/core"
	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
)

// Rules are the deterministic thresholds of the 5xx signal. They come from
// Owner-approved configuration; nothing the layer observes changes them.
type Rules struct {
	// MinSamples is the fewest requests in a window for its rate to count.
	MinSamples int64
	// ErrorRateThreshold is the 5xx share above which a window is bad.
	ErrorRateThreshold float64
	// MinDelta is how far above its baseline a bad window's rate must be.
	MinDelta float64
	// ConsecutiveWindows is how many bad windows in a row make a proposal.
	ConsecutiveWindows int
	// MinBaselineWindows is how many healthy windows a baseline needs.
	MinBaselineWindows int
	// BaselineWindows is how many healthy windows the baseline remembers.
	BaselineWindows int
	// MaxEndpoints bounds how many endpoints are tracked, so endpoint names
	// chosen by clients cannot exhaust memory.
	MaxEndpoints int
}

// DefaultRules are the starting parameters of the brief: 5xx above 2% and
// more than one percentage point above the baseline, with enough samples,
// in two consecutive windows.
func DefaultRules() Rules {
	return Rules{
		MinSamples: 100, ErrorRateThreshold: 0.02, MinDelta: 0.01, ConsecutiveWindows: 2,
		MinBaselineWindows: 3, BaselineWindows: 30, MaxEndpoints: 1000,
	}
}

// Validate rejects rules that could make every window look bad or good.
func (r Rules) Validate() error {
	switch {
	case r.MinSamples < 1, r.ConsecutiveWindows < 1, r.MinBaselineWindows < 1, r.MaxEndpoints < 1:
		return invalid("decision rules need at least one sample, window, baseline window and endpoint")
	case r.BaselineWindows < r.MinBaselineWindows:
		return invalid("the baseline must remember at least as many windows as it needs")
	case !(r.ErrorRateThreshold > 0 && r.ErrorRateThreshold < 1), !(r.MinDelta > 0 && r.MinDelta < 1):
		return invalid("the error-rate threshold and the minimum delta must lie strictly between 0 and 1")
	}
	return nil
}

// Typed question identifiers. Each is answered from the evidence alone.
const (
	QuestionEnoughSamples     = "enough_samples"
	QuestionBaselineKnown     = "baseline_known"
	QuestionAboveThreshold    = "above_threshold"
	QuestionWorseThanBaseline = "worse_than_baseline"
	QuestionRecurring         = "recurring"
	QuestionDismissed         = "dismissed"
)

const (
	yes     = "yes"
	no      = "no"
	unknown = "unknown"
)

// Questions returns the typed questions asked of every window, in order.
func Questions() []pkgdecision.Question {
	yn := []string{yes, no}
	return []pkgdecision.Question{
		{ID: QuestionEnoughSamples, Text: "Does the window hold enough requests to judge?", Options: yn, Weight: 1},
		{ID: QuestionBaselineKnown, Text: "Does the endpoint have a baseline of healthy windows?", Options: yn, Weight: 1},
		{ID: QuestionAboveThreshold, Text: "Is the 5xx rate above the threshold?", Options: yn, Weight: 1},
		{ID: QuestionWorseThanBaseline, Text: "Is the 5xx rate worse than the baseline by more than the minimum delta?", Options: []string{yes, no, unknown}, Weight: 1},
		{ID: QuestionRecurring, Text: "Has the endpoint been bad for the required consecutive windows?", Options: yn, Weight: 1},
		{ID: QuestionDismissed, Text: "Did a person dismiss the proposal for this condition?", Options: yn, Weight: 1},
	}
}

// answers are the typed answers for one window, by question.
type answers map[string]string

func (a answers) list() []pkgdecision.Answer {
	out := make([]pkgdecision.Answer, 0, len(a))
	for _, q := range Questions() {
		out = append(out, pkgdecision.Answer{QuestionID: q.ID, Value: a[q.ID]})
	}
	return out
}

// verdictFor maps typed answers to a verdict. Only a recurring regression
// over a known baseline with enough samples creates work.
func verdictFor(a answers, rising bool) (pkgdecision.Verdict, string) {
	switch {
	case a[QuestionEnoughSamples] == no:
		return pkgdecision.VerdictCollectMoreEvidence, "too_few_samples"
	case a[QuestionAboveThreshold] == no && rising:
		return pkgdecision.VerdictObserve, "rising_below_threshold"
	case a[QuestionAboveThreshold] == no:
		return pkgdecision.VerdictIgnore, "below_threshold"
	case a[QuestionBaselineKnown] == no:
		return pkgdecision.VerdictCollectMoreEvidence, "no_baseline"
	case a[QuestionWorseThanBaseline] != yes:
		return pkgdecision.VerdictObserve, "not_worse_than_baseline"
	case a[QuestionRecurring] == no:
		return pkgdecision.VerdictInvestigate, "awaiting_recurrence"
	case a[QuestionDismissed] == yes:
		return pkgdecision.VerdictStop, "dismissed"
	}
	return pkgdecision.VerdictPropose, "recurring_regression"
}

func invalid(message string) error { return core.NewError(core.CodeInvalidArgument, message) }

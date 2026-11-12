package decision

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
)

// Metric is the name of the signal this layer judges.
const Metric = "http.server.5xx_rate"

// maxEndpointLen bounds an endpoint name; longer names fail data quality.
const maxEndpointLen = 256

// endpointState is what the layer remembers about one endpoint.
type endpointState struct {
	// history holds recent healthy windows with enough samples. Bad windows
	// are kept out, so an incident does not raise its own baseline.
	history []Window
	streak  int
}

// assessment is the deterministic judgment of one window.
type assessment struct {
	verdict  pkgdecision.Verdict
	reason   string
	answers  answers
	evidence pkgdecision.Evidence
}

// assess runs data quality, baseline, typed questions and evidence for one
// window and updates the endpoint's state. name is the redacted endpoint.
func (r Rules) assess(w Window, name string, st *endpointState, dismissed bool) assessment {
	if reason := dataQuality(w); reason != "" {
		st.streak = 0
		verdict := pkgdecision.VerdictIgnore
		if w.Reset {
			verdict = pkgdecision.VerdictCollectMoreEvidence
		}
		return assessment{verdict: verdict, reason: reason, evidence: r.evidence(w, name, 0, false, 0)}
	}
	baseline, known := r.baseline(st.history)
	rate := w.Rate()
	enough := w.Requests >= r.MinSamples
	above := rate > r.ErrorRateThreshold
	worse := unknown
	if known {
		worse = yesNo(rate-baseline > r.MinDelta)
	}
	breach := enough && above && worse == yes
	if breach {
		st.streak++
	} else {
		st.streak = 0
	}
	a := answers{
		QuestionEnoughSamples:     yesNo(enough),
		QuestionBaselineKnown:     yesNo(known),
		QuestionAboveThreshold:    yesNo(above),
		QuestionWorseThanBaseline: worse,
		QuestionRecurring:         yesNo(st.streak >= r.ConsecutiveWindows),
		QuestionDismissed:         yesNo(dismissed),
	}
	verdict, reason := verdictFor(a, known && rate > baseline)
	ev := r.evidence(w, name, baseline, known, st.streak)
	if enough && !breach {
		st.history = append(st.history, w)
		if extra := len(st.history) - r.BaselineWindows; extra > 0 {
			st.history = append(st.history[:0], st.history[extra:]...)
		}
	}
	return assessment{verdict: verdict, reason: reason, answers: a, evidence: ev}
}

// dataQuality names the first reason the window cannot be judged, or "".
func dataQuality(w Window) string {
	switch {
	case !validEndpoint(w.Endpoint):
		return "invalid_endpoint"
	case w.ServerErrors > w.Requests || w.Requests < 0 || w.ServerErrors < 0:
		return "inconsistent_counts"
	case !w.To.After(w.From):
		return "empty_time_range"
	case w.Reset:
		return "counter_reset"
	case w.Requests == 0:
		return "no_activity"
	}
	return ""
}

func validEndpoint(name string) bool {
	return strings.TrimSpace(name) != "" && len(name) <= maxEndpointLen && utf8.ValidString(name) &&
		!strings.ContainsFunc(name, unicode.IsControl)
}

// baseline is the pooled 5xx rate of the remembered healthy windows.
func (r Rules) baseline(history []Window) (float64, bool) {
	if len(history) < r.MinBaselineWindows {
		return 0, false
	}
	var requests, errs int64
	for _, w := range history {
		requests += w.Requests
		errs += w.ServerErrors
	}
	return float64(errs) / float64(requests), true
}

// evidence records what the verdict rests on, traceable to the edge window.
func (r Rules) evidence(w Window, name string, baseline float64, known bool, streak int) pkgdecision.Evidence {
	trend := pkgdecision.TrendUnknown
	switch rate := w.Rate(); {
	case !known:
	case rate > baseline:
		trend = pkgdecision.TrendRising
	case rate < baseline:
		trend = pkgdecision.TrendFalling
	default:
		trend = pkgdecision.TrendFlat
	}
	// Confidence grows with the sample count and saturates at four times
	// the minimum.
	confidence := min(1, float64(max(w.Requests, 0))/float64(4*r.MinSamples))
	return pkgdecision.Evidence{
		Metric: Metric, Value: w.Rate(), Baseline: baseline, Trend: trend,
		SampleCount: max(w.Requests, 0), From: w.From, To: w.To,
		UserImpact: fmt.Sprintf("%d of %d requests failed with a server error", max(w.ServerErrors, 0), max(w.Requests, 0)),
		Recurrence: streak, Confidence: confidence,
		SourceRefs: []string{fmt.Sprintf("edge:counters:%q@%s/%s", name, w.From.UTC().Format(timeRef), w.To.UTC().Format(timeRef))},
	}
}

const timeRef = "2006-01-02T15:04:05Z"

func yesNo(b bool) string {
	if b {
		return yes
	}
	return no
}

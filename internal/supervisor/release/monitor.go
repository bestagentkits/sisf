package release

import (
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/pkg/deploy"
)

// Decision is what the monitor concludes from one window.
type Decision string

const (
	// DecisionContinue means the window gave no reason to stop.
	DecisionContinue Decision = "continue"
	// DecisionRollback means the release must be restored to the LKG.
	DecisionRollback Decision = "rollback"
	// DecisionLowTraffic means the window had too few samples to judge by
	// its error rate; the synthetic suite must judge it instead.
	DecisionLowTraffic Decision = "low_traffic"
)

// WindowReport is the evidence of one observation window.
type WindowReport struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Percent int       `json:"percent"`
	// Samples are the responses the candidate served in the window, the
	// denominator of its error rate, and ServerErrors those that were 5xx.
	Samples      int64            `json:"samples"`
	ServerErrors int64            `json:"server_errors"`
	ErrorRate    float64          `json:"error_rate"`
	BaselineRate float64          `json:"baseline_rate"`
	Healthy      bool             `json:"healthy"`
	Critical     int64            `json:"critical_errors"`
	Breach       bool             `json:"breach"`
	Decision     Decision         `json:"decision"`
	Synthetic    *SyntheticResult `json:"synthetic,omitempty"`
	Note         string           `json:"note,omitempty"`
}

// Monitor applies the automatic rollback rule window by window:
//
//   - a failed health check or a new critical-level error rolls back at once;
//   - a window with fewer than MinSamples responses draws no conclusion from
//     its error rate and breaks a run of breaching windows;
//   - a window breaches when its 5xx rate exceeds ErrorRateThreshold and
//     exceeds the baseline by more than MinDelta; Consecutive breaching
//     windows in a row roll back, and a clean window resets the count.
type Monitor struct {
	cfg      MonitorConfig
	breaches int
}

// NewMonitor returns a monitor applying cfg; unset parameters take the
// defaults.
func NewMonitor(cfg MonitorConfig) *Monitor { return &Monitor{cfg: cfg.withDefaults()} }

// Judge decides one window of the candidate against the baseline error rate
// of the revision it replaces.
func (m *Monitor) Judge(candidate deploy.Metrics, baseline float64) (WindowReport, Decision) {
	report := WindowReport{
		From: candidate.From, To: candidate.To, Samples: candidate.Requests, ServerErrors: candidate.ServerErrors,
		BaselineRate: baseline, Healthy: candidate.Healthy, Critical: candidate.CriticalErrors,
	}
	decide := func(d Decision, note string) (WindowReport, Decision) {
		report.Decision, report.Note = d, note
		return report, d
	}
	if !candidate.Healthy || candidate.CriticalErrors > 0 {
		return decide(DecisionRollback, "critical health: the health check failed or a critical error was logged")
	}
	rate, ok := candidate.ErrorRate()
	if !ok || candidate.Requests < m.cfg.MinSamples {
		m.breaches = 0
		return decide(DecisionLowTraffic, fmt.Sprintf("%d responses, fewer than the %d a window needs", candidate.Requests, m.cfg.MinSamples))
	}
	report.ErrorRate = rate
	report.Breach = rate > m.cfg.ErrorRateThreshold && rate-baseline > m.cfg.MinDelta
	if !report.Breach {
		m.breaches = 0
		return decide(DecisionContinue, "")
	}
	m.breaches++
	if m.breaches >= m.cfg.Consecutive {
		return decide(DecisionRollback, fmt.Sprintf("the 5xx rate %.4f breached the threshold and the baseline %.4f for %d windows in a row", rate, baseline, m.breaches))
	}
	return decide(DecisionContinue, fmt.Sprintf("breaching window %d of %d", m.breaches, m.cfg.Consecutive))
}

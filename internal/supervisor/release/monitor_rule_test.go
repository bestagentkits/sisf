package release

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/deploy"
)

// metrics is one window of a revision.
func metrics(requests, errors int64, healthy bool) deploy.Metrics {
	return deploy.Metrics{Requests: requests, ServerErrors: errors, Healthy: healthy}
}

func TestTheMonitorNeedsBothTheThresholdAndTheRiseOverTheBaseline(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		window   deploy.Metrics
		baseline float64
		breach   bool
	}{
		"above threshold and baseline":        {metrics(1000, 50, true), 0.01, true},
		"above threshold, baseline as high":   {metrics(1000, 50, true), 0.045, false},
		"rise over baseline, below threshold": {metrics(1000, 15, true), 0.0, false},
		"exactly at the threshold":            {metrics(1000, 20, true), 0.0, false},
	}
	for name, c := range cases {
		report, _ := NewMonitor(MonitorConfig{}).Judge(c.window, c.baseline)
		if report.Breach != c.breach {
			t.Errorf("%s: breach %v, want %v (rate %.3f)", name, report.Breach, c.breach, report.ErrorRate)
		}
	}
}

func TestTooFewSamplesDrawNoConclusionFromAFailedRequest(t *testing.T) {
	t.Parallel()
	m := NewMonitor(MonitorConfig{})
	for i := range 3 {
		report, decision := m.Judge(metrics(1, 1, true), 0)
		if decision != DecisionLowTraffic || report.Breach {
			t.Fatalf("window %d with one failed request decided %s (breach %v), want low traffic", i+1, decision, report.Breach)
		}
	}
	if _, decision := m.Judge(metrics(0, 0, true), 0); decision != DecisionLowTraffic {
		t.Fatalf("a window without traffic decided %s, want low traffic", decision)
	}
}

func TestALowTrafficWindowBreaksARunOfBreaches(t *testing.T) {
	t.Parallel()
	m := NewMonitor(MonitorConfig{})
	m.Judge(metrics(1000, 100, true), 0)
	m.Judge(metrics(10, 5, true), 0)
	if _, decision := m.Judge(metrics(1000, 100, true), 0); decision != DecisionContinue {
		t.Fatalf("breaches separated by a low-traffic window decided %s, want continue", decision)
	}
}

func TestAnUnhealthyWindowRollsBackWhateverItsTraffic(t *testing.T) {
	t.Parallel()
	for name, window := range map[string]deploy.Metrics{
		"failed health check": metrics(0, 0, false),
		"critical error":      {Requests: 5000, Healthy: true, CriticalErrors: 1},
	} {
		if _, decision := NewMonitor(MonitorConfig{}).Judge(window, 0); decision != DecisionRollback {
			t.Errorf("%s decided %s, want rollback", name, decision)
		}
	}
}

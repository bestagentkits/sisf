package conformance

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// A threshold can be well formed and still never be reached, which switches
// the rollback off as surely as leaving it out.
func TestARollbackThresholdCanBeReached(t *testing.T) {
	t.Parallel()
	unreachable := []struct {
		name   string
		change func(r *manifest.Release)
		path   string
	}{
		{"an error rate no release exceeds", func(r *manifest.Release) { r.Rollback.ErrorRate = 1 }, "release.rollback.error_rate"},
		{"an error rate above half of all requests", func(r *manifest.Release) { r.Rollback.ErrorRate = 0.51 }, "release.rollback.error_rate"},
		{"a rise over baseline no release exceeds", func(r *manifest.Release) { r.Rollback.BaselineDelta = 1 }, "release.rollback.baseline_delta"},
		{"more samples than a window carries", func(r *manifest.Release) { r.Rollback.MinSamples = 1_000_000_000_000 }, "release.rollback.min_samples"},
		{"one sample more than the bound", func(r *manifest.Release) { r.Rollback.MinSamples = 1_000_001 }, "release.rollback.min_samples"},
		{"a window of one nanosecond", func(r *manifest.Release) { r.Rollback.Window = 1 }, "release.rollback.window"},
		{"a window just under a minute", func(r *manifest.Release) { r.Rollback.Window = manifest.Duration(59 * time.Second) }, "release.rollback.window"},
		{"a window longer than a day", func(r *manifest.Release) { r.Rollback.Window = manifest.Duration(25 * time.Hour) }, "release.rollback.window"},
		{"a window of years", func(r *manifest.Release) { r.Rollback.Window = manifest.Duration(1<<63 - 1) }, "release.rollback.window"},
		{"windows that add up to more than a day", func(r *manifest.Release) { r.Rollback.ConsecutiveWindows = 145 }, "release.rollback.consecutive_windows"},
		{"more windows than can be counted", func(r *manifest.Release) { r.Rollback.ConsecutiveWindows = 1<<31 - 1 }, "release.rollback.consecutive_windows"},
		{"a hold of one nanosecond", func(r *manifest.Release) { r.LowTraffic.MinHold = 1 }, "release.low_traffic.min_hold"},
	}
	for _, tc := range unreachable {
		for mode, valid := range bothModes {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				t.Parallel()
				m := valid(t)
				tc.change(&m.Release)
				report := Check(m)
				wantOnly(t, report, RuleReleaseBounds, tc.path)
				if len(report.Findings) != 1 {
					t.Fatalf("one setting out of range is one finding, got %+v", report.Findings)
				}
			})
		}
	}

	// The bounds themselves, and the brief's starting values, are in range.
	reachable := map[string]func(r *manifest.Release){
		"the starting values":    func(*manifest.Release) {},
		"half of all requests":   func(r *manifest.Release) { r.Rollback.ErrorRate, r.Rollback.BaselineDelta = 0.5, 0.5 },
		"a million samples":      func(r *manifest.Release) { r.Rollback.MinSamples = 1_000_000 },
		"a window of one minute": func(r *manifest.Release) { r.Rollback.Window = manifest.Duration(time.Minute) },
		"one window of a day": func(r *manifest.Release) {
			r.Rollback.Window, r.Rollback.ConsecutiveWindows = manifest.Duration(24*time.Hour), 1
		},
		"windows that add up to one day":  func(r *manifest.Release) { r.Rollback.ConsecutiveWindows = 144 },
		"a hold of one minute":            func(r *manifest.Release) { r.LowTraffic.MinHold = manifest.Duration(time.Minute) },
		"a hold of a week, which is safe": func(r *manifest.Release) { r.LowTraffic.MinHold = manifest.Duration(7 * 24 * time.Hour) },
	}
	for name, change := range reachable {
		m := docker(t)
		change(&m.Release)
		if report := Check(m); !report.Conformant() {
			t.Errorf("%s must be accepted, got %+v", name, report.Findings)
		}
	}
}

// With little traffic a release is judged by synthetic runs alone, so the
// factory does not publish by itself without a suite to run.
func TestSelfPublishingNeedsASyntheticSuite(t *testing.T) {
	t.Parallel()
	for mode, valid := range bothModes {
		for _, suite := range []string{"", "  "} {
			m := valid(t)
			m.Release.LowTraffic.SyntheticSuite = suite
			report := Check(m)
			wantOnly(t, report, RuleSyntheticSuiteMissing, "release.low_traffic.synthetic_suite")
			if len(report.Findings) != 1 {
				t.Fatalf("%s: a missing suite is one finding, got %+v", mode, report.Findings)
			}
		}
		// When a person publishes every release, nothing relies on the suite.
		m := valid(t)
		m.Release.LowTraffic.SyntheticSuite, m.Release.Units = "", []manifest.UnitName{}
		if report := Check(m); !report.Conformant() {
			t.Fatalf("%s: nothing self-published needs no suite, got %+v", mode, report.Findings)
		}
	}
}

func TestTheNormalPipelinePublishesOnlyUnitsThatMayChangeWithoutAPerson(t *testing.T) {
	t.Parallel()
	refused := []manifest.UnitName{manifest.UnitQARunner, manifest.UnitEdge, manifest.UnitState, manifest.UnitArtifacts}
	for _, unit := range refused {
		for mode, valid := range bothModes {
			t.Run(string(unit)+"/"+mode, func(t *testing.T) {
				t.Parallel()
				m := valid(t)
				m.Release.Units = append(m.Release.Units, unit)
				report := Check(m)
				wantOnly(t, report, RuleReleaseUnit, "release.units[1]")
				if len(report.Findings) != 1 {
					t.Fatalf("one unit that may not be published is one finding, got %+v", report.Findings)
				}
				// The finding says why this unit is refused and why control
				// and dispatcher are not.
				for _, want := range []string{fmt.Sprintf("%q", unit), "control and dispatcher", "governance cycle"} {
					if !strings.Contains(report.Findings[0].Message, want) {
						t.Errorf("the finding must say %q, got %q", want, report.Findings[0].Message)
					}
				}
			})
		}
	}

	// Every refused unit of one list is reported.
	m := docker(t)
	m.Release.Units = []manifest.UnitName{manifest.UnitApp, manifest.UnitQARunner, manifest.UnitEdge, manifest.UnitControl,
		manifest.UnitDispatcher, manifest.UnitState}
	report := Check(m)
	for _, path := range []string{"release.units[1]", "release.units[2]", "release.units[5]"} {
		wantOnly(t, report, RuleReleaseUnit, path)
	}
	if len(report.Findings) != 3 {
		t.Fatalf("the judge, the traffic switch and the state store are three findings, got %+v", report.Findings)
	}

	// The supervisor is refused by the rule of its own boundary.
	m = docker(t)
	m.Release.Units = append(m.Release.Units, manifest.UnitSupervisor)
	wantOnly(t, Check(m), RuleSupervisorDigestChange, "release.units")

	// The application and the factory's own Zone 2 and Zone 3 code may be
	// published by the pipeline.
	for mode, valid := range bothModes {
		m := valid(t)
		m.Release.Units = []manifest.UnitName{manifest.UnitApp, manifest.UnitControl, manifest.UnitDispatcher, manifest.UnitRunner}
		if report := Check(m); !report.Conformant() || !report.SelfUpdate {
			t.Errorf("%s: app, control, dispatcher and runner may be self-published, got %+v", mode, report.Findings)
		}
	}
}

// Unlimited means no cap set by the factory. The Owner then either sets an
// alert threshold or says that none is wanted; leaving both out is neither.
func TestAnUnlimitedBudgetStatesItsAlertThresholdOrThatNoneIsWanted(t *testing.T) {
	t.Parallel()
	for mode, valid := range bothModes {
		m := valid(t)
		m.Budget.AlertThresholdMicros = 0
		report := Check(m)
		wantOnly(t, report, RuleBudgetAlert, "budget.alert_threshold_micros")
		if len(report.Findings) != 1 || !strings.Contains(report.Findings[0].Fix, "alert_off: true") {
			t.Fatalf("%s: want one finding that names both ways out, got %+v", mode, report.Findings)
		}

		m.Budget.AlertOff = true
		if report := Check(m); !report.Conformant() {
			t.Fatalf("%s: an alert explicitly declined is a decision, got %+v", mode, report.Findings)
		}

		// A capped budget stops by itself, so the alert is optional there.
		m = valid(t)
		m.Budget = manifest.Budget{Mode: core.BudgetCapped, CapMicros: 100_000_000, Period: manifest.PeriodMonthly}
		if report := Check(m); !report.Conformant() {
			t.Fatalf("%s: a capped budget needs no alert decision, got %+v", mode, report.Findings)
		}
	}
}

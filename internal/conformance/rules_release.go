package conformance

import (
	"fmt"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Bounds past which a release setting no longer does what it configures.
// They are wide on purpose: the thresholds themselves are calibrated per
// deployment, and these only refuse values no calibration arrives at.
const (
	// minObservation is the shortest span a release is observed over. Less
	// than this holds no traffic and no synthetic run worth judging.
	minObservation = time.Minute
	// maxRollbackDecision is the longest a failing release may run before
	// the rollback is decided: window times consecutive_windows.
	maxRollbackDecision = 24 * time.Hour
	// maxRollbackRate is the highest share of failing requests, and the
	// highest rise over baseline, a rollback may wait for.
	maxRollbackRate = 0.5
	// maxMinSamples is the most requests a window may need before its error
	// rate counts.
	maxMinSamples = 1_000_000
)

// checkRelease rejects release settings that are well formed yet switch off
// what they configure: a span over which nothing is observed, a rollback
// threshold no release can cross, self-publishing without the synthetic
// suite that judges a release with little traffic, a unit the pipeline may
// not publish, and a canary that has no steps, does not grow or never ends.
func (c *checker) checkRelease() {
	c.checkReleaseSpans()
	c.checkRollbackBounds()
	c.checkSelfPublishing()
	c.checkCanary()
}

// span renders a bound the way a manifest writes it: "1m", not "1m0s".
func span(d time.Duration) string {
	return strings.TrimSuffix(strings.TrimSuffix(d.String(), "0s"), "0m")
}

// checkReleaseSpans requires the rollback window and the low-traffic hold
// to be long enough to observe anything.
func (c *checker) checkReleaseSpans() {
	release := c.m.Release
	spans := []struct {
		path     string
		span     manifest.Duration
		observes string
	}{
		{"release.rollback.window", release.Rollback.Window, "the error rate is measured over this window before a rollback is decided, so no window means no measurement"},
		{"release.low_traffic.min_hold", release.LowTraffic.MinHold, "a release with little traffic is held this long under synthetic checks, so no hold means it passes unobserved"},
	}
	for _, s := range spans {
		switch {
		case s.span.Std() <= 0:
			c.add(RuleReleaseDuration, s.path,
				"set "+s.path+" to a positive span, such as 10m",
				"%s is not positive; %s", s.path, s.observes)
		case s.span.Std() < minObservation:
			c.add(RuleReleaseBounds, s.path,
				fmt.Sprintf("set %s to %s or longer, such as 10m", s.path, span(minObservation)),
				"%s is shorter than %s; %s, and a span this short sees next to nothing", s.path, span(minObservation), s.observes)
		}
	}
}

// checkRollbackBounds refuses rollback thresholds that no failing release
// reaches in time. A rollback needs the error rate and its rise over
// baseline both exceeded, on enough samples, for consecutive windows; each
// of those can be set so high that the rollback never happens.
func (c *checker) checkRollbackBounds() {
	rollback := c.m.Release.Rollback
	never := func(key, bound, why string) {
		at := "release.rollback." + key
		c.add(RuleReleaseBounds, at, fmt.Sprintf("set %s to %s", at, bound), "%s is %s", at, why)
	}
	if rollback.ErrorRate > maxRollbackRate {
		never("error_rate", fmt.Sprintf("%v or less, such as 0.02", maxRollbackRate),
			"above the share of failing requests any release should be left to reach; a rollback starts only when the error rate exceeds it, so at 1 it never starts")
	}
	if rollback.BaselineDelta > maxRollbackRate {
		never("baseline_delta", fmt.Sprintf("%v or less, such as 0.01", maxRollbackRate),
			"a rise over the baseline that a failing release hardly reaches; a rollback needs it exceeded as well as the error rate, so at 1 it never starts")
	}
	if rollback.MinSamples > maxMinSamples {
		never("min_samples", fmt.Sprintf("%d or less, such as 100", maxMinSamples),
			"more requests than a window is expected to carry; below it the error rate does not count, so real failures never trigger a rollback")
	}
	window := rollback.Window.Std()
	switch {
	case window > maxRollbackDecision:
		never("window", fmt.Sprintf("%s or less, such as 10m", span(maxRollbackDecision)),
			"so long that a failing release serves traffic for more than a day before its error rate is judged once")
	case window > 0 && rollback.ConsecutiveWindows > int(maxRollbackDecision/window):
		never("consecutive_windows", fmt.Sprintf("a count that, times release.rollback.window, stays within %s, such as 2", span(maxRollbackDecision)),
			"so many windows that a failing release serves traffic for more than a day before the rollback is decided")
	}
}

// notSelfPublishable gives, for each unit the normal release pipeline may
// not publish, the reason. The supervisor is refused by the rule of its own
// boundary.
var notSelfPublishable = map[manifest.UnitName]string{
	manifest.UnitQARunner:  "it is the judge of every candidate, and a candidate that could replace its judge would score itself",
	manifest.UnitEdge:      "it splits traffic between the stable release and the candidate and polices what the sandbox sends out, so a candidate that replaced it could keep the traffic and lift the egress policy",
	manifest.UnitState:     "it holds the data that outlives every release, and switching back a version does not restore data a release changed",
	manifest.UnitArtifacts: "it holds the evidence, logs and snapshots that releases are judged and restored by, and a candidate that replaced it could rewrite them",
}

// checkSelfPublishing judges what the factory publishes without a person.
//
// The pipeline may publish app, control, dispatcher and runner. The factory
// is meant to repair its own Zone 2 and Zone 3 code, so control (a
// read-only dashboard page) and dispatcher (a dispatch fault) are allowed:
// each release of them is judged by the qa-runner and can be undone by the
// supervisor, and neither of those two is in the list. The judge, the
// traffic switch and the durable stores change only through a governance
// cycle approved by a person. Publishing anything needs a synthetic suite,
// because a release that gets little traffic is judged by nothing else.
func (c *checker) checkSelfPublishing() {
	release := c.m.Release
	for i, unit := range release.Units {
		reason, refused := notSelfPublishable[unit]
		if !refused {
			continue
		}
		c.add(RuleReleaseUnit, fmt.Sprintf("release.units[%d]", i),
			fmt.Sprintf("remove %s from release.units; it changes only through a governance cycle approved by a person", unit),
			"release.units lets the normal release pipeline publish %q by itself, but %s. The pipeline may publish app, control, dispatcher and runner: control and dispatcher are code of Zone 3 and Zone 2 that the factory is meant to repair by itself, each release of them is judged by the independent qa-runner and can be undone by the supervisor, and what enforces policy inside them still changes only through a governance cycle, whatever this list says", unit, reason)
	}
	if len(release.Units) > 0 && strings.TrimSpace(release.LowTraffic.SyntheticSuite) == "" {
		c.add(RuleSyntheticSuiteMissing, "release.low_traffic.synthetic_suite",
			"name the synthetic suite that covers the flows a release must keep working, or empty release.units so that a person publishes",
			"release.units lets the factory publish by itself, but release.low_traffic.synthetic_suite names no suite; when real traffic stays below release.rollback.min_samples a release is judged by synthetic runs alone, and without them nothing judges it")
	}
}

// checkCanary requires a canary release to name steps that grow from a
// share of the traffic to all of it.
func (c *checker) checkCanary() {
	if c.m.Release.Strategy != manifest.StrategyCanary {
		return
	}
	steps := c.m.Release.CanarySteps
	if len(steps) == 0 {
		c.add(RuleCanaryStepsMissing, "release.canary_steps",
			"list the traffic percentages of the canary, such as [5, 25, 100], or set release.strategy to blue_green",
			"release.strategy is canary, but release.canary_steps names no step, so nothing says how much traffic a candidate takes before all of it")
		return
	}
	growing := steps[0] < 100
	for i := 1; i < len(steps); i++ {
		growing = growing && steps[i] > steps[i-1]
	}
	switch {
	case !growing:
		c.add(RuleCanarySteps, "release.canary_steps",
			"list percentages that strictly increase from a first step below 100, such as [5, 25, 100]",
			"release.canary_steps is %v; a canary starts with a share of the traffic and only grows, otherwise a candidate takes all traffic before it is observed or is moved back and forth", steps)
	case steps[len(steps)-1] != 100:
		c.add(RuleCanaryIncomplete, "release.canary_steps",
			"end release.canary_steps with 100, such as [5, 25, 100]",
			"the last step of release.canary_steps is not 100, so the release never finishes: the candidate keeps a share of the traffic and the version it replaces keeps the rest, with no step at which one of them is promoted")
	}
}

// checkModelRoutes requires at least one model route. Code reads the route
// of a step from this table and no model is ever asked to choose its own, so
// an empty table leaves every step without a model.
func (c *checker) checkModelRoutes() {
	if len(c.m.ModelRoutes.Routes) == 0 {
		c.add(RuleModelRoutesEmpty, "model_routes.routes",
			"add a route for each kind of step the factory runs, such as coding and qa",
			"model_routes.routes is empty, so no kind of step has a model to run on")
	}
}

package conformance

import (
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkRelease rejects release settings that are well formed yet switch off
// what they configure: a span of zero over which nothing is observed, and a
// canary that has no steps or does not grow.
func (c *checker) checkRelease() {
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
		if s.span.Std() <= 0 {
			c.add(RuleReleaseDuration, s.path,
				"set "+s.path+" to a positive span, such as 10m",
				"%s is not positive; %s", s.path, s.observes)
		}
	}
	if release.Strategy != manifest.StrategyCanary {
		return
	}
	steps := release.CanarySteps
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
	if !growing {
		c.add(RuleCanarySteps, "release.canary_steps",
			"list percentages that strictly increase from a first step below 100, such as [5, 25, 100]",
			"release.canary_steps is %v; a canary starts with a share of the traffic and only grows, otherwise a candidate takes all traffic before it is observed or is moved back and forth", steps)
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

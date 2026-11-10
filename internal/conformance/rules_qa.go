package conformance

import (
	"fmt"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkQA keeps QA independent of the coding agent it judges: QA runs on
// another provider or model, in a workspace of its own, with credentials of
// its own. An agent that could reach its judge's model, files or credentials
// would be scoring its own work.
func (c *checker) checkQA() {
	c.checkQARoutes()
	c.checkQAWorkspace()
	c.checkQACredentials()
}

// modelChoice is one route a kind of step can run on, with its place in the
// manifest.
type modelChoice struct {
	path  string
	route core.ModelRoute
}

// modelChoices lists the route of a kind of step followed by the routes it
// escalates to.
func (c *checker) modelChoices(kind core.StepKind) []modelChoice {
	choices := []modelChoice{{fmt.Sprintf("model_routes.routes.%s", kind), c.m.ModelRoutes.Routes[kind]}}
	for i, route := range c.m.ModelRoutes.Escalation[kind] {
		choices = append(choices, modelChoice{fmt.Sprintf("model_routes.escalation.%s[%d]", kind, i), route})
	}
	return choices
}

// sameModel reports whether two routes are one provider and one model.
// Effort is ignored, because more effort on one model shares its blind
// spots, and so are letter case and surrounding space, because they do not
// make another model.
func sameModel(a, b core.ModelRoute) bool {
	same := func(x, y string) bool { return strings.EqualFold(strings.TrimSpace(x), strings.TrimSpace(y)) }
	return same(a.Provider, b.Provider) && same(a.Model, b.Model)
}

// checkQARoutes requires a model route for the coding step and for the QA
// step, and requires that no route QA can run on, escalations included, is
// the provider and model of a route the coding agent can run on. A
// deployment with a single route says so with model_routes.same_model_route;
// the acknowledgement adds no independence, it records that the assurance is
// reduced.
func (c *checker) checkQARoutes() {
	routes := c.m.ModelRoutes.Routes
	if len(routes) == 0 {
		return // an empty table is reported as such
	}
	missing := false
	for _, kind := range []core.StepKind{core.StepCoding, core.StepQA} {
		if route, routed := routes[kind]; !routed || route.IsZero() {
			missing = true
			c.add(RuleQARouteMissing, fmt.Sprintf("model_routes.routes.%s", kind),
				fmt.Sprintf("add model_routes.routes.%s with a provider and a model", kind),
				"the %s step has no model route, so nothing says whether QA runs on another provider or model than the coding agent", kind)
		}
	}
	if missing {
		return
	}
	const fix = "route the qa step to another provider or model than every coding route, or set model_routes.same_model_route: true to state that this deployment has a single model route"
	for j, judge := range c.modelChoices(core.StepQA) {
		for i, coder := range c.modelChoices(core.StepCoding) {
			if !sameModel(judge.route, coder.route) {
				continue
			}
			if c.m.ModelRoutes.SameModelRoute {
				c.sameModelRoute = true
				continue
			}
			// The finding stands at the route that was added to the pair of
			// starting routes: an escalation step when one is involved.
			at, other := judge.path, coder.path
			if j == 0 && i > 0 {
				at, other = coder.path, judge.path
			}
			c.addOnce(RuleQASameModelRoute, at, fix,
				"the route at %s is the provider and model of the route at %s, so QA and the coding agent share the same blind spots; with model_routes.same_model_route the manifest is accepted at reduced assurance: every QA receipt records it and policy may require the Owner's approval in place of self-publishing", at, other)
		}
	}
}

// checkQAWorkspace forbids the runner and the qa-runner to share files: no
// volume is mounted by both, and on one machine no directory of one lies in
// a directory of the other. QA reads a baseline the coding agent cannot
// alter and leaves nothing the coding agent can read.
func (c *checker) checkQAWorkspace() {
	runner, qa := c.m.Units[manifest.UnitRunner], c.m.Units[manifest.UnitQARunner]
	const fix = "mount in units.qa-runner a volume and a directory that units.runner does not use, such as qa-workspaces"
	for i, held := range qa.Mounts {
		at := fmt.Sprintf("units.qa-runner.mounts[%d]", i)
		for j, other := range runner.Mounts {
			switch {
			case held.Volume == other.Volume:
				c.addOnce(RuleQASharedWorkspace, at, fix,
					"the qa-runner mounts volume %q, which the runner mounts too (units.runner.mounts[%d]); QA judges the coding agent's work from a workspace of its own, so that the coding agent can neither alter what QA reads nor read what QA leaves", held.Volume, j)
			case c.m.Mode == manifest.ModeStandalone && held.Path != "" && other.Path != "" && overlap(held.Path, other.Path):
				c.addOnce(RuleQASharedWorkspace, at, fix,
					"the qa-runner mounts volume %q at a directory that is, holds or lies inside the runner's (units.runner.mounts[%d]); on one machine they are the same files, so the coding agent can alter what QA reads", held.Volume, j)
			}
		}
	}
}

// checkQACredentials requires the qa-runner to run with a credential set
// different from the runner's. A unit uses the credential set it names, or
// its zone's when it names none; two units of Zone 2 that name none
// therefore run with the same one.
func (c *checker) checkQACredentials() {
	runner, declared := c.m.Units[manifest.UnitRunner]
	qa, alsoDeclared := c.m.Units[manifest.UnitQARunner]
	agent := c.m.Components.Agent
	if !declared || !alsoDeclared || agent == nil {
		return // a missing unit or zone is reported as such
	}
	effective := func(unit manifest.Unit) string {
		if unit.CredentialsRef != "" {
			return unit.CredentialsRef
		}
		return agent.CredentialsRef
	}
	if !strings.EqualFold(effective(runner), effective(qa)) {
		return
	}
	message := "the qa-runner names no credential set of its own, so it runs with the credentials of Zone 2 that the runner uses"
	if qa.CredentialsRef != "" {
		message = "the qa-runner references the credential set the runner uses"
	}
	// The reference is not repeated: what stands in its place may be a secret.
	c.add(RuleQASharedCredentials, "units.qa-runner.credentials_ref",
		"set units.qa-runner.credentials_ref to "+manifest.SecretRefForm+" that names a credential set only the qa-runner uses",
		"%s; independent QA runs with an identity and credentials different from the runner's, so that a coding agent that leaks or misuses its credentials cannot act as its own judge", message)
}

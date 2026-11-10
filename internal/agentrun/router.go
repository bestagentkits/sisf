// Package agentrun decides which model an agent step runs on, how a run that
// falls short is retried, and what a fresh agent is told when it takes a run
// over.
//
// The decisions are made by code from the model routes in the factory
// manifest, never by asking a model. A run starts cheap, at the route of its
// step kind; only a run that fails QA (or whose agent fails) is run again,
// one rung up a fixed ladder. A model or effort changes only between
// attempts, at a state boundary, and the next attempt receives a structured
// handoff summary (scope, definition of done, decisions, evidence), never the
// previous conversation.
package agentrun

import (
	"slices"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// effortOrder lists effort levels from least to most. An effort not listed
// here cannot be raised or lowered by the ladder.
var effortOrder = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// Router picks model routes. It is immutable and safe for concurrent use.
type Router struct {
	routes manifest.ModelRoutes
}

// NewRouter checks the route table and returns a router over it.
func NewRouter(routes manifest.ModelRoutes) (*Router, error) {
	if routes.MaxEscalations < 0 {
		return nil, core.NewError(core.CodeInvalidArgument, "max_escalations must not be negative")
	}
	for step, route := range routes.Routes {
		if !step.Valid() {
			return nil, core.NewError(core.CodeInvalidArgument, "the route table names an unknown step kind "+string(step))
		}
		if err := route.Validate(); err != nil {
			return nil, err
		}
	}
	for step, ladder := range routes.Escalation {
		if !step.Valid() {
			return nil, core.NewError(core.CodeInvalidArgument, "the escalation table names an unknown step kind "+string(step))
		}
		for _, route := range ladder {
			if err := route.Validate(); err != nil {
				return nil, err
			}
		}
	}
	return &Router{routes: routes}, nil
}

// Route returns the route a step of the given kind starts on. A kind with no
// configured route is an error: no default is invented.
func (r *Router) Route(step core.StepKind) (core.ModelRoute, error) {
	route, ok := r.routes.Routes[step]
	if !ok {
		return core.ModelRoute{}, core.NewError(core.CodeNotFound, "no model route is configured for step kind "+string(step))
	}
	return route, nil
}

// MaxEscalations is how many times a run may be moved up the ladder.
func (r *Router) MaxEscalations() int { return r.routes.MaxEscalations }

// RungKind says what one ladder step changes.
type RungKind string

const (
	// RungVerify keeps the route and requires the agent to check its own work.
	RungVerify RungKind = "verify"
	// RungEffort raises the effort on the same model.
	RungEffort RungKind = "effort"
	// RungModel moves to a larger model at a lower effort than the last.
	RungModel RungKind = "model"
)

// Position is how far up the ladder a run has been taken.
type Position struct {
	// Next is the index of the next rung to try.
	Next int `json:"next"`
	// Used is how many escalations the run has spent.
	Used int `json:"used"`
}

// Rung is the next attempt's shape.
type Rung struct {
	Kind   RungKind
	Route  core.ModelRoute
	Verify bool
}

// Escalate returns the next rung for a run of the given step kind that just
// fell short on current, and the position after taking it. The ladder is:
//
//  1. make sure the agent verifies its own work, on the same route;
//  2. then each entry of the step's configured escalation list in order: more
//     effort on the same model, or a larger model, started at a lower effort
//     than the one just used. When no list is configured the only further
//     rung is one level more effort on the same model;
//
// bounded by max_escalations. When no rung is left the error is
// LIMIT_EXCEEDED.
func (r *Router) Escalate(step core.StepKind, current core.ModelRoute, verifying bool, pos Position) (Rung, Position, error) {
	if pos.Used >= r.routes.MaxEscalations {
		return Rung{}, pos, core.NewError(core.CodeLimitExceeded, "the run has used all of its escalations")
	}
	list := r.routes.Escalation[step]
	for idx := pos.Next; ; idx++ {
		rung, ok := r.rung(idx, list, current, verifying)
		if !ok {
			return Rung{}, pos, core.NewError(core.CodeLimitExceeded, "the escalation ladder has no rung left for step kind "+string(step))
		}
		if rung.Route == current && rung.Verify == verifying {
			// A rung that changes nothing would only repeat the last run.
			continue
		}
		return rung, Position{Next: idx + 1, Used: pos.Used + 1}, nil
	}
}

// rung computes the idx-th rung from the last attempt's route.
func (r *Router) rung(idx int, list []core.ModelRoute, current core.ModelRoute, verifying bool) (Rung, bool) {
	if idx == 0 {
		return Rung{Kind: RungVerify, Route: current, Verify: true}, true
	}
	if len(list) == 0 {
		if idx != 1 {
			return Rung{}, false
		}
		raised, ok := shift(current.Effort, +1)
		if !ok {
			return Rung{}, false
		}
		current.Effort = raised
		return Rung{Kind: RungEffort, Route: current, Verify: true}, true
	}
	if idx-1 >= len(list) {
		return Rung{}, false
	}
	next := list[idx-1]
	if current.SameModel(next) {
		return Rung{Kind: RungEffort, Route: next, Verify: true}, true
	}
	// A larger model starts below the effort just used, so it is not asked to
	// think harder than the cheaper model was.
	if lower, ok := shift(current.Effort, -1); ok {
		at := effortIndex(next.Effort)
		if next.Effort == "" || (at >= 0 && at >= effortIndex(current.Effort)) {
			next.Effort = lower
		}
	}
	return Rung{Kind: RungModel, Route: next, Verify: true}, true
}

func effortIndex(effort string) int { return slices.Index(effortOrder, effort) }

// shift moves effort by delta levels, reporting false when it cannot move.
func shift(effort string, delta int) (string, bool) {
	i := effortIndex(effort)
	if i < 0 || i+delta < 0 || i+delta >= len(effortOrder) {
		return "", false
	}
	return effortOrder[i+delta], true
}

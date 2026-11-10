package qa

import (
	"github.com/bestagentkits/sisf/pkg/core"
)

// RouteResolver is implemented by a judge that may run on another model
// than the route the evaluator chose, for example a configured model name
// on the same provider. The evaluator asks it which route the judgment
// will actually run on, and records that route on the receipt, so a
// receipt never names a model that did not judge.
type RouteResolver interface {
	// ResolveRoute returns the route the judge runs on when the evaluator
	// chose chosen for a candidate written on coder. It refuses a route
	// that would share the coder's provider and model when chosen does not.
	ResolveRoute(chosen, coder core.ModelRoute) (core.ModelRoute, error)
}

// actualRoute is the route the receipt records: the chosen one, or the one
// the judge resolves it to. A judge may move QA off the coder's model, but
// never onto it: that would undo the evaluator's route rule.
func (e *Evaluator) actualRoute(chosen routeChoice, coder core.ModelRoute) (routeChoice, error) {
	resolver, ok := e.cfg.Judge.(RouteResolver)
	if !ok {
		return chosen, nil
	}
	actual, err := resolver.ResolveRoute(chosen.route, coder)
	if err != nil {
		return routeChoice{}, err
	}
	if err := actual.Validate(); err != nil {
		return routeChoice{}, err
	}
	same := actual.SameModel(coder)
	if same && !chosen.same {
		return routeChoice{}, denied("the judge would run on the coder's provider and model, which the evaluator's route does not")
	}
	return routeChoice{route: actual, same: same}, nil
}

package agentrun_test

import (
	"testing"

	"github.com/bestagentkits/sisf/internal/agentrun"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func route(provider, model, effort string) core.ModelRoute {
	return core.ModelRoute{Provider: provider, Model: model, Effort: effort}
}

// routes is the table the factory ships: coding starts cheap and has two
// escalation rungs.
func routes() manifest.ModelRoutes {
	return manifest.ModelRoutes{
		Routes: map[core.StepKind]core.ModelRoute{
			core.StepIntake:   route("anthropic", "small", "low"),
			core.StepCoding:   route("anthropic", "mid", "low"),
			core.StepPlanning: route("anthropic", "large", "medium"),
			core.StepReadOnly: route("anthropic", "small", ""),
			core.StepQA:       route("openai", "mid", "low"),
		},
		Escalation: map[core.StepKind][]core.ModelRoute{
			core.StepCoding: {route("anthropic", "mid", "high"), route("anthropic", "large", "low")},
		},
		MaxEscalations: 3,
	}
}

func newRouter(t *testing.T, mutate func(*manifest.ModelRoutes)) *agentrun.Router {
	t.Helper()
	r := routes()
	if mutate != nil {
		mutate(&r)
	}
	router, err := agentrun.NewRouter(r)
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	return router
}

func TestEachStepKindStartsOnItsConfiguredRouteChosenByCode(t *testing.T) {
	t.Parallel()
	router := newRouter(t, nil)
	want := map[core.StepKind]core.ModelRoute{
		core.StepIntake: route("anthropic", "small", "low"), core.StepCoding: route("anthropic", "mid", "low"),
		core.StepPlanning: route("anthropic", "large", "medium"), core.StepReadOnly: route("anthropic", "small", ""),
		core.StepQA: route("openai", "mid", "low"),
	}
	for step, expected := range want {
		got, err := router.Route(step)
		if err != nil || got != expected {
			t.Errorf("%s: got %v, %v want %v", step, got, err, expected)
		}
	}
	sparse := newRouter(t, func(r *manifest.ModelRoutes) { delete(r.Routes, core.StepPlanning) })
	if _, err := sparse.Route(core.StepPlanning); !core.HasCode(err, core.CodeNotFound) {
		t.Errorf("a step kind with no route must not get an invented default: %v", err)
	}
}

func TestTheRouteTableIsCheckedWhenTheRouterIsBuilt(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*manifest.ModelRoutes){
		"route without a model":   func(r *manifest.ModelRoutes) { r.Routes[core.StepCoding] = route("anthropic", "", "low") },
		"unknown step kind":       func(r *manifest.ModelRoutes) { r.Routes["advisor"] = route("anthropic", "mid", "") },
		"bad escalation route":    func(r *manifest.ModelRoutes) { r.Escalation[core.StepCoding][0] = route("", "mid", "") },
		"negative escalation cap": func(r *manifest.ModelRoutes) { r.MaxEscalations = -1 },
	}
	for name, mutate := range cases {
		r := routes()
		mutate(&r)
		if _, err := agentrun.NewRouter(r); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Errorf("%s: want INVALID_ARGUMENT, got %v", name, err)
		}
	}
}

// The ladder: verify first, then more effort, then a larger model started at
// a lower effort than the one just used.
func TestTheLadderVerifiesFirstThenRaisesEffortThenMovesToALargerModelAtLowerEffort(t *testing.T) {
	t.Parallel()
	router := newRouter(t, nil)
	current, verifying, pos := route("anthropic", "mid", "low"), false, agentrun.Position{}
	steps := []struct {
		kind  agentrun.RungKind
		route core.ModelRoute
	}{
		{agentrun.RungVerify, route("anthropic", "mid", "low")},
		{agentrun.RungEffort, route("anthropic", "mid", "high")},
		{agentrun.RungModel, route("anthropic", "large", "low")},
	}
	for i, want := range steps {
		rung, next, err := router.Escalate(core.StepCoding, current, verifying, pos)
		if err != nil {
			t.Fatalf("rung %d: %v", i+1, err)
		}
		if rung.Kind != want.kind || rung.Route != want.route || !rung.Verify {
			t.Errorf("rung %d: got %+v, want %s on %s with verification", i+1, rung, want.kind, want.route)
		}
		if next.Used != i+1 {
			t.Errorf("rung %d: %d escalations used, want %d", i+1, next.Used, i+1)
		}
		current, verifying, pos = rung.Route, rung.Verify, next
	}
	if _, _, err := router.Escalate(core.StepCoding, current, verifying, pos); !core.HasCode(err, core.CodeLimitExceeded) {
		t.Errorf("past the configured ladder: want LIMIT_EXCEEDED, got %v", err)
	}
}

func TestALargerModelNeverStartsAtOrAboveTheEffortJustUsed(t *testing.T) {
	t.Parallel()
	cases := []struct{ used, configured, want string }{
		{"high", "high", "medium"},      // same effort: start one level below
		{"high", "xhigh", "medium"},     // higher configured effort: start one level below
		{"high", "low", "low"},          // already lower: the configured one stands
		{"high", "", "medium"},          // provider default: made explicit and lower
		{"minimal", "medium", "medium"}, // nothing lower exists: the configured one stands
	}
	for _, c := range cases {
		router := newRouter(t, func(r *manifest.ModelRoutes) {
			r.Escalation[core.StepCoding] = []core.ModelRoute{route("anthropic", "large", c.configured)}
		})
		rung, _, err := router.Escalate(core.StepCoding, route("anthropic", "mid", c.used), true, agentrun.Position{Next: 1})
		if err != nil || rung.Kind != agentrun.RungModel || rung.Route.Effort != c.want {
			t.Errorf("used %q configured %q: got %+v, %v want effort %q", c.used, c.configured, rung, err, c.want)
		}
	}
}

func TestWithoutAnEscalationListTheOnlyFurtherRungIsOneLevelMoreEffort(t *testing.T) {
	t.Parallel()
	router := newRouter(t, func(r *manifest.ModelRoutes) { r.Escalation = nil })
	rung, pos, err := router.Escalate(core.StepCoding, route("anthropic", "mid", "low"), false, agentrun.Position{})
	if err != nil || rung.Kind != agentrun.RungVerify {
		t.Fatalf("first rung %+v %v", rung, err)
	}
	rung, pos, err = router.Escalate(core.StepCoding, rung.Route, rung.Verify, pos)
	if err != nil || rung.Kind != agentrun.RungEffort || rung.Route.Effort != "medium" {
		t.Fatalf("second rung %+v %v: want one level more effort", rung, err)
	}
	if _, _, err := router.Escalate(core.StepCoding, rung.Route, rung.Verify, pos); !core.HasCode(err, core.CodeLimitExceeded) {
		t.Errorf("no larger model is invented: want LIMIT_EXCEEDED, got %v", err)
	}
}

func TestEscalationsAreCapped(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{0, 1} {
		router := newRouter(t, func(r *manifest.ModelRoutes) { r.MaxEscalations = limit })
		current, verifying, pos := route("anthropic", "mid", "low"), false, agentrun.Position{}
		used := 0
		for {
			rung, next, err := router.Escalate(core.StepCoding, current, verifying, pos)
			if err != nil {
				if !core.HasCode(err, core.CodeLimitExceeded) {
					t.Fatalf("cap %d: %v", limit, err)
				}
				break
			}
			used++
			current, verifying, pos = rung.Route, rung.Verify, next
		}
		if used != limit {
			t.Errorf("cap %d: %d escalations were allowed", limit, used)
		}
	}
}

func TestARungThatChangesNothingIsSkipped(t *testing.T) {
	t.Parallel()
	// The first configured route equals the current one once verification is
	// on, so it would only repeat the last run.
	router := newRouter(t, func(r *manifest.ModelRoutes) {
		r.Escalation[core.StepCoding] = []core.ModelRoute{route("anthropic", "mid", "low"), route("anthropic", "mid", "high")}
	})
	rung, _, err := router.Escalate(core.StepCoding, route("anthropic", "mid", "low"), true, agentrun.Position{Next: 1})
	if err != nil || rung.Route != route("anthropic", "mid", "high") {
		t.Errorf("got %+v, %v: the no-op rung should be skipped", rung, err)
	}
}

package qa_test

import (
	"context"
	"testing"

	evaluator "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestQARunsOnADifferentRouteWhenTheDeploymentHasTwo(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	c := candidate()
	// The pinned QA route is the coder's model at another effort, which shares
	// its blind spots; the other configured route is taken instead.
	c.Policy.ModelRoute = core.ModelRoute{Provider: " Anthropic", Model: "MID", Effort: "high"}
	result, err := e.build(t).Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	r := result.Receipt
	if r.QAModelRoute != qaRoute || r.SameModelRoute || r.CodingModelRoute != coderRoute {
		t.Fatalf("QA ran on %s next to coder %s (same=%v)", r.QAModelRoute, r.CodingModelRoute, r.SameModelRoute)
	}
	if judged := e.judge.Judged(issue); len(judged) != 1 || judged[0].ModelRoute != qaRoute || judged[0].Identity != qaIdentity {
		t.Fatalf("the judge was asked as %+v", judged)
	}
}

func TestASingleRouteNeedsTheOwnersAcknowledgement(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.cfg.Routes = []core.ModelRoute{coderRoute}
	c := candidate()
	c.Policy.ModelRoute = coderRoute
	_, err := e.build(t).Evaluate(context.Background(), c)
	wantCode(t, err, core.CodePolicyHold)
	if len(e.judge.Judged(issue)) != 0 {
		t.Fatal("QA must not run on the coder's route without the Owner's acknowledgement")
	}
}

func TestASharedRouteIsDisclosedAndFollowsThePolicyAction(t *testing.T) {
	t.Parallel()
	cases := map[evaluator.SameRouteAction]evaluator.Action{
		"":                               evaluator.ActionOwnerApproval,
		evaluator.SameRouteOwnerApproval: evaluator.ActionOwnerApproval,
		evaluator.SameRouteSelfPublish:   evaluator.ActionRelease,
	}
	for action, want := range cases {
		t.Run(string(action), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.cfg.Routes, e.cfg.SameRouteAcknowledged, e.cfg.SameRouteAction = []core.ModelRoute{coderRoute}, true, action
			c := candidate()
			c.Policy.ModelRoute = coderRoute
			result, err := e.build(t).Run(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Receipt.SameModelRoute || result.Receipt.QAModelRoute != coderRoute {
				t.Fatalf("a shared route must be disclosed, got %+v", result.Receipt)
			}
			if !result.Passed || result.Action != want {
				t.Fatalf("action = %s (passed=%v), want %s", result.Action, result.Passed, want)
			}
		})
	}
}

func TestAFailedCandidateOnASharedRouteStillGoesBackToDevelopment(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.cfg.Routes, e.cfg.SameRouteAcknowledged = nil, true
	e.cfg.Gates[0] = gate{name: e.cfg.Gates[0].Name(), passed: false}
	c := candidate()
	c.Policy.ModelRoute = coderRoute
	result, err := e.build(t).Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed || result.Action != evaluator.ActionReturnToDevelopment {
		t.Fatalf("result = %+v", result)
	}
}

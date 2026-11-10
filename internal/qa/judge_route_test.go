package qa_test

import (
	"context"
	"testing"

	evaluator "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// resolvingJudge scores as the fake judge does and says it runs on
// resolved, or fails to say.
type resolvingJudge struct {
	qa.Judge
	resolved core.ModelRoute
	err      error
	asked    []core.ModelRoute
	scored   int
}

func (j *resolvingJudge) ResolveRoute(chosen, _ core.ModelRoute) (core.ModelRoute, error) {
	j.asked = append(j.asked, chosen)
	return j.resolved, j.err
}

func (j *resolvingJudge) Score(ctx context.Context, req qa.JudgeRequest) (qa.Scorecard, error) {
	j.scored++
	return j.Judge.Score(ctx, req)
}

func TestTheReceiptRecordsTheRouteTheJudgeActuallyRanOn(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	actual := core.ModelRoute{Provider: "openai", Model: "gpt-judge", Effort: "low"}
	judge := &resolvingJudge{Judge: e.judge, resolved: actual}
	e.cfg.Judge = judge
	result, err := e.build(t).Run(context.Background(), candidate())
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.QAModelRoute != actual || result.Receipt.SameModelRoute || !result.Passed {
		t.Fatalf("the receipt records %+v (same route %v), want the route the judge ran on", result.Receipt.QAModelRoute, result.Receipt.SameModelRoute)
	}
	// The judge was asked on the route the evaluator chose.
	if len(judge.asked) != 1 || judge.asked[0] != qaRoute || e.judge.Judged(issue)[0].ModelRoute != qaRoute {
		t.Fatalf("the judge was asked on %v", judge.asked)
	}
	// The receipt's route is signed: changing it breaks the signature.
	tampered := result.Receipt
	tampered.QAModelRoute = qaRoute
	if err := evaluator.VerifyReceipt(context.Background(), e.verifier, tampered, factory, candidate(), e.clock.Now()); !core.HasCode(err, core.CodeSignatureInvalid) {
		t.Fatalf("a receipt with the policy route in place of the actual one verified: %v", err)
	}
}

func TestAJudgeCannotMoveQAOntoTheCodersModel(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	judge := &resolvingJudge{Judge: e.judge, resolved: core.ModelRoute{Provider: "Anthropic", Model: "MID"}}
	e.cfg.Judge = judge
	_, err := e.build(t).Run(context.Background(), candidate())
	wantCode(t, err, core.CodePermissionDenied)
	if judge.scored != 0 {
		t.Fatal("the judge scored on the coder's model")
	}

	failing := &resolvingJudge{Judge: e.judge, err: core.NewError(core.CodeInvalidArgument, "no such model")}
	e.cfg.Judge = failing
	_, err = e.build(t).Run(context.Background(), candidate())
	wantCode(t, err, core.CodeInvalidArgument)
}

// With one acknowledged route, a judge on a model of its own is no longer
// on the coder's: the receipt says so and no Owner approval is needed.
func TestAJudgeOffTheSharedRouteIsRecordedAsIndependent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.cfg.Routes, e.cfg.SameRouteAcknowledged = []core.ModelRoute{coderRoute}, true
	c := candidate()
	c.Policy.ModelRoute = coderRoute
	e.cfg.Judge = &resolvingJudge{Judge: e.judge, resolved: core.ModelRoute{Provider: "anthropic", Model: "large"}}
	result, err := e.build(t).Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.SameModelRoute || result.Action != evaluator.ActionRelease || result.Receipt.QAModelRoute.Model != "large" {
		t.Fatalf("result %+v", result)
	}
}

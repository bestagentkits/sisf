package qa_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

func TestTheBaselineIsReadOnlyAndLockedToTheCandidatesPins(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*qa.Baseline){
		"writable":             func(b *qa.Baseline) { b.ReadOnly = false },
		"another commit":       func(b *qa.Baseline) { b.CommitSHA = candidateSHA },
		"another policy":       func(b *qa.Baseline) { b.PolicySHA = baselineSHA },
		"another suite":        func(b *qa.Baseline) { b.SuiteDigest = otherDigest },
		"not pinned to commit": func(b *qa.Baseline) { b.CommitSHA = "main" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			mutate(&e.baselines.first)
			_, err := e.build(t).Evaluate(context.Background(), candidate())
			wantCode(t, err, core.CodePermissionDenied)
			if len(e.judge.Judged(issue)) != 0 {
				t.Fatal("no judge may be asked from a baseline that is not locked")
			}
		})
	}
}

func TestABaselineThatChangesDuringTheEvaluationIssuesNoReceipt(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	changed := e.baselines.first
	changed.SuiteDigest = otherDigest
	e.baselines.after = &changed
	receipt, err := e.build(t).Evaluate(context.Background(), candidate())
	wantCode(t, err, core.CodeTampered)
	if !receipt.Signature.IsZero() {
		t.Fatal("a receipt was signed over a baseline that changed")
	}
}

func TestABaselineThatCannotBeReadHoldsTheEvaluation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.baselines.err = core.NewError(core.CodeUnavailable, "store down")
	_, err := e.build(t).Evaluate(context.Background(), candidate())
	wantCode(t, err, core.CodeUnavailable)
}

func TestAnUnbuiltCandidateIsNotScored(t *testing.T) {
	t.Parallel()
	t.Run("no digest", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		c := candidate()
		c.Digest = ""
		_, err := e.build(t).Evaluate(context.Background(), c)
		wantCode(t, err, core.CodeInvalidArgument)
	})
	t.Run("build not finished", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		e.cfg.Builds = builds{}
		_, err := e.build(t).Evaluate(context.Background(), candidate())
		wantCode(t, err, core.CodeConflict)
		if len(e.judge.Judged(issue)) != 0 {
			t.Fatal("an unbuilt candidate reached the judge")
		}
	})
	t.Run("digest of another build", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		e.cfg.Builds = builds{candidateSHA: otherDigest}
		_, err := e.build(t).Evaluate(context.Background(), candidate())
		wantCode(t, err, core.CodeTampered)
	})
}

func TestAJudgeThatCannotScoreIssuesNoReceipt(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if err := e.judge.Script(agent.RoleQA, issue, fake.Crash(0)); err != nil {
		t.Fatal(err)
	}
	receipt, err := e.build(t).Evaluate(context.Background(), candidate())
	wantCode(t, err, core.CodeUnavailable)
	if !receipt.Signature.IsZero() {
		t.Fatal("a receipt was issued without a score")
	}
}

// overScoringJudge returns points above a category's weight.
type overScoringJudge struct{}

func (overScoringJudge) Score(_ context.Context, req qa.JudgeRequest) (qa.Scorecard, error) {
	points := scores(30)
	points[qa.CategorySecurity] = 99
	return qa.Scorecard{Points: points}, nil
}

func TestAScorecardOutsideTheRubricIssuesNoReceipt(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.cfg.Judge = overScoringJudge{}
	_, err := e.build(t).Evaluate(context.Background(), candidate())
	wantCode(t, err, core.CodeInvalidArgument)
}

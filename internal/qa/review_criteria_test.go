package qa_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	evaluator "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

func TestTheRepositoryReviewCriteriaStateTheStartingRubric(t *testing.T) {
	t.Parallel()
	rubric, err := evaluator.LoadRubric(context.Background(), reviewFiles{policySHA: repositoryReview(t)}, policySHA)
	if err != nil {
		t.Fatal(err)
	}
	want := qa.DefaultRubric()
	if rubric.PassScore != 85 || len(rubric.Weights) != len(want.Weights) {
		t.Fatalf("rubric = %+v", rubric)
	}
	for category, weight := range want.Weights {
		if rubric.Weights[category] != weight {
			t.Errorf("weight of %s = %d, want %d", category, rubric.Weights[category], weight)
		}
	}
}

func TestReviewCriteriaQACannotUseHoldTheEvaluation(t *testing.T) {
	t.Parallel()
	review := repositoryReview(t)
	cases := map[string]string{
		"weight lowered":       strings.Replace(review, "| Security and auth | 25 |", "| Security and auth | 5 |", 1),
		"category missing":     strings.Replace(review, "| Observability and rollback | 10 |\n", "", 1),
		"category repeated":    strings.Replace(review, "| UX, performance, output | 15 |", "| UX, performance, output | 15 |\n| UX performance output | 15 |", 1),
		"no pass score":        strings.ReplaceAll(review, "score of at least 85", "a good score"),
		"two pass scores":      review + "\nA hotfix needs a score of at least 70.\n",
		"pass score above 100": strings.ReplaceAll(review, "score of at least 85", "score of at least 150"),
	}
	for name, text := range cases {
		if text == review {
			t.Fatalf("%s: the fixture did not change the criteria", name)
		}
		_, err := evaluator.LoadRubric(context.Background(), reviewFiles{policySHA: text}, policySHA)
		if !core.HasCode(err, core.CodePolicyHold) {
			t.Errorf("%s: want %s, got %v", name, core.CodePolicyHold, err)
		}
	}
	if _, err := evaluator.LoadRubric(context.Background(), reviewFiles{}, policySHA); !core.HasCode(err, core.CodePolicyHold) {
		t.Errorf("missing criteria: want %s, got %v", core.CodePolicyHold, err)
	}
	if _, err := evaluator.LoadRubric(context.Background(), reviewFiles{"main": review}, "main"); !core.HasCode(err, core.CodePolicyHold) {
		t.Errorf("criteria at a branch: want %s, got %v", core.CodePolicyHold, err)
	}
}

func TestThePassScoreIsTheOneAtThePinnedCommit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	stricter := strings.ReplaceAll(repositoryReview(t), "score of at least 85", "score of at least 90")
	// The commit the candidate pins is stricter than the current file; the
	// pinned commit decides.
	e.cfg.Review = reviewFiles{policySHA: stricter, baselineSHA: repositoryReview(t)}
	if err := e.judge.Script(agent.RoleQA, issue, fake.Score(scores(17))); err != nil { // 87 points
		t.Fatal(err)
	}
	result, err := e.build(t).Run(context.Background(), candidate())
	if err != nil {
		t.Fatal(err)
	}
	if result.Rubric.PassScore != 90 || result.Passed || result.Receipt.Score != 87 {
		t.Fatalf("87 points against a pass score of 90 passed: %+v", result)
	}
}

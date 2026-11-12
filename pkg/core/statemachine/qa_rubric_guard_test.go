package statemachine

import (
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// The score counts only when it was earned in the rubric of the review
// criteria: every category once, out of its own weight.

func TestAPassScoreCannotBeReachedOutsideTheRubric(t *testing.T) {
	t.Parallel()
	rubricOnly := func(p *QAProof, rubric ...core.QACheck) {
		checks := slices.DeleteFunc(p.Receipt.Checks, func(c core.QACheck) bool { return c.Kind == core.CheckRubric })
		p.Receipt.Checks = append(checks, rubric...)
		p.Receipt.Score = 0
		for _, c := range rubric {
			p.Receipt.Score += c.Points
		}
	}
	full := core.QACheck{Name: core.RubricCorrectness, Kind: core.CheckRubric, Passed: true, Points: 30, MaxPoints: 30}
	cases := map[string]func(*QAProof){
		"one category scored 90 out of 100": func(p *QAProof) {
			rubricOnly(p, core.QACheck{Name: core.RubricCorrectness, Kind: core.CheckRubric, Points: 90, MaxPoints: 100})
		},
		"one invented category scored 90 out of 100": func(p *QAProof) {
			rubricOnly(p, core.QACheck{Name: "overall", Kind: core.CheckRubric, Points: 90, MaxPoints: 100})
		},
		"one category recorded three times and another at zero": func(p *QAProof) {
			rubricOnly(p, full, full, full, core.QACheck{Name: core.RubricSecurity, Kind: core.CheckRubric, MaxPoints: 25})
		},
		"a category is scored twice": func(p *QAProof) {
			p.Receipt.Checks = append(p.Receipt.Checks, core.QACheck{Name: core.RubricObservability, Kind: core.CheckRubric, MaxPoints: 10})
		},
		"a category is left out": func(p *QAProof) {
			p.Receipt.Checks = slices.DeleteFunc(p.Receipt.Checks, func(c core.QACheck) bool {
				return c.Name == core.RubricObservability
			})
			p.Receipt.Score -= 9
		},
		"a category was given a higher ceiling": func(p *QAProof) {
			check := rubricCheck(p, core.RubricExperience)
			check.Points, check.MaxPoints = 20, 20
			p.Receipt.Score += 8
		},
		"a category was given a lower ceiling": func(p *QAProof) {
			rubricCheck(p, core.RubricSecurity).MaxPoints = 23
		},
		"an extra category adds points": func(p *QAProof) {
			p.Receipt.Checks = append(p.Receipt.Checks, core.QACheck{Name: "style", Kind: core.CheckRubric, Points: 5, MaxPoints: 5})
			p.Receipt.Score += 5
		},
		"a category is named in another letter case": func(p *QAProof) {
			rubricCheck(p, core.RubricContract).Name = "Contract_Data_Safety"
		},
	}
	for name, mutate := range cases {
		err := checkQAWith(mutate)
		requireGuardFailed(t, name, err, GuardQAPass)
		if detailsOf(err)["rubric_category"] == "" {
			t.Errorf("%s: the refusal names no rubric category: %v", name, detailsOf(err))
		}
	}
}

func TestAFullScoreInEveryCategoryPasses(t *testing.T) {
	t.Parallel()
	if err := checkQAWith(func(p *QAProof) { scored(p, 100) }); err != nil {
		t.Fatalf("a receipt with every category at its maximum was refused: %v", err)
	}
}

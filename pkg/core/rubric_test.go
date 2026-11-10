package core

import (
	"slices"
	"testing"
)

// rubricIndex finds the scored result of the named rubric category.
func rubricIndex(r QAReceipt, category string) int {
	return slices.IndexFunc(r.Checks, func(c QACheck) bool { return c.Kind == CheckRubric && c.Name == category })
}

// withRubric replaces the rubric results of a passing receipt and sets the
// score to the points they add up to.
func withRubric(rubric ...QACheck) QAReceipt {
	receipt := passingReceipt()
	receipt.Checks = slices.DeleteFunc(receipt.Checks, func(c QACheck) bool { return c.Kind == CheckRubric })
	receipt.Checks = append(receipt.Checks, rubric...)
	receipt.Score = receipt.RubricPoints()
	return receipt
}

func TestTheRubricIsTheFiveCategoriesOfTheReviewCriteriaAddingUpToOneHundred(t *testing.T) {
	t.Parallel()
	want := []RubricCategory{
		{"correctness_regression", 30},
		{"security_auth", 25},
		{"contract_data_safety", 20},
		{"ux_performance_output", 15},
		{"observability_rollback", 10},
	}
	if got := RubricCategories(); !slices.Equal(got, want) {
		t.Fatalf("RubricCategories() = %v, want %v", got, want)
	}
	total := 0
	for _, category := range RubricCategories() {
		total += category.MaxPoints
	}
	if total != 100 {
		t.Fatalf("the rubric adds up to %d points, want 100", total)
	}
	RubricCategories()[0].MaxPoints = 100
	if RubricCategories()[0].MaxPoints != 30 {
		t.Fatal("a caller changed the weight of a rubric category for everyone")
	}
}

func TestAPassScoreIsNotReachedByOneCategoryScoredOutOfOneHundred(t *testing.T) {
	t.Parallel()
	for name, check := range map[string]QACheck{
		"a rubric category":    {Name: RubricCorrectness, Kind: CheckRubric, Passed: true, Points: 90, MaxPoints: 100},
		"an invented category": {Name: "overall", Kind: CheckRubric, Passed: true, Points: 90, MaxPoints: 100},
	} {
		receipt := withRubric(check)
		if receipt.Score != 90 {
			t.Fatalf("%s: the fixture scores %d, want 90", name, receipt.Score)
		}
		err := receipt.CheckPass(85)
		if !HasCode(err, CodeGuardFailed) {
			t.Errorf("%s at 90/100: want %s, got %v", name, CodeGuardFailed, err)
		}
		if named := errorDetails(err)["rubric_category"]; named != check.Name {
			t.Errorf("%s: the refusal names rubric category %q", name, named)
		}
	}
}

func TestAPassScoreIsNotReachedByRecordingOneCategorySeveralTimes(t *testing.T) {
	t.Parallel()
	full := QACheck{Name: RubricCorrectness, Kind: CheckRubric, Passed: true, Points: 30, MaxPoints: 30}
	receipt := withRubric(full, full, full, QACheck{Name: RubricSecurity, Kind: CheckRubric, MaxPoints: 25})
	if receipt.Score != 90 {
		t.Fatalf("the fixture scores %d, want 90", receipt.Score)
	}
	err := receipt.CheckPass(85)
	if !HasCode(err, CodeGuardFailed) {
		t.Fatalf("correctness at 30/30 three times: want %s, got %v", CodeGuardFailed, err)
	}
	if named := errorDetails(err)["rubric_category"]; named != RubricCorrectness {
		t.Errorf("the refusal names rubric category %q, want the repeated one", named)
	}
}

func TestEveryRubricCategoryMustBeScoredOnceOutOfItsOwnWeight(t *testing.T) {
	t.Parallel()
	for _, category := range RubricCategories() {
		cases := map[string]func(*QAReceipt){
			"left out": func(r *QAReceipt) {
				i := rubricIndex(*r, category.Name)
				r.Checks = slices.Delete(r.Checks, i, i+1)
			},
			"scored twice": func(r *QAReceipt) {
				r.Checks = append(r.Checks, QACheck{Name: category.Name, Kind: CheckRubric, MaxPoints: category.MaxPoints})
			},
			"given a higher ceiling": func(r *QAReceipt) { r.Checks[rubricIndex(*r, category.Name)].MaxPoints++ },
			"given a lower ceiling":  func(r *QAReceipt) { r.Checks[rubricIndex(*r, category.Name)].MaxPoints-- },
			"given no ceiling":       func(r *QAReceipt) { r.Checks[rubricIndex(*r, category.Name)].MaxPoints = 0 },
			"scored above its weight": func(r *QAReceipt) {
				r.Checks[rubricIndex(*r, category.Name)].Points = category.MaxPoints + 1
			},
			"scored below zero": func(r *QAReceipt) { r.Checks[rubricIndex(*r, category.Name)].Points = -1 },
			"recorded as a hard gate": func(r *QAReceipt) {
				r.Checks[rubricIndex(*r, category.Name)].Kind = CheckHardGate
			},
		}
		for name, mutate := range cases {
			receipt := passingReceipt()
			mutate(&receipt)
			receipt.Score = receipt.RubricPoints()
			err := receipt.CheckPass(1)
			if !HasCode(err, CodeGuardFailed) {
				t.Errorf("%s %s: want %s even at the lowest pass score, got %v", category.Name, name, CodeGuardFailed, err)
				continue
			}
			if named := errorDetails(err)["rubric_category"]; named != category.Name {
				t.Errorf("%s %s: the refusal names rubric category %q", category.Name, name, named)
			}
		}
	}
}

func TestACategoryOutsideTheRubricEarnsNothing(t *testing.T) {
	t.Parallel()
	cases := map[string]QACheck{
		"an extra category with points":      {Name: "style", Kind: CheckRubric, Points: 5, MaxPoints: 5},
		"an extra category without points":   {Name: "style", Kind: CheckRubric},
		"a category in another letter case":  {Name: "Security_Auth", Kind: CheckRubric, MaxPoints: 25},
		"a category padded with a space":     {Name: "security_auth ", Kind: CheckRubric, MaxPoints: 25},
		"a hard gate recorded as a category": {Name: "secrets", Kind: CheckRubric, Points: 5, MaxPoints: 5},
		"an unnamed category":                {Kind: CheckRubric, Points: 5, MaxPoints: 5},
	}
	for name, extra := range cases {
		receipt := passingReceipt()
		receipt.Checks = append(receipt.Checks, extra)
		receipt.Score = receipt.RubricPoints()
		if err := receipt.CheckPass(1); !HasCode(err, CodeGuardFailed) {
			t.Errorf("%s: want %s, got %v", name, CodeGuardFailed, err)
		}
	}
}

func TestARubricShortcutNeverOutranksAFailedHardGate(t *testing.T) {
	t.Parallel()
	// The gates are judged first, so the refusal of a receipt that is wrong
	// in both ways names the gate.
	receipt := withRubric(QACheck{Name: RubricCorrectness, Kind: CheckRubric, Points: 100, MaxPoints: 100})
	receipt.Checks[gateIndex(receipt, "secrets")].Passed = false
	err := receipt.CheckPass(85)
	if !HasCode(err, CodeGuardFailed) || errorDetails(err)["gate"] != "secrets" {
		t.Fatalf("want the failed gate to be the reason, got %v with %v", err, errorDetails(err))
	}
}

func TestAReceiptWithEveryCategoryAtItsWeightScoresOneHundred(t *testing.T) {
	t.Parallel()
	receipt := passingReceipt()
	for i := range receipt.Checks {
		if receipt.Checks[i].Kind == CheckRubric {
			receipt.Checks[i].Points = receipt.Checks[i].MaxPoints
		}
	}
	receipt.Score = receipt.RubricPoints()
	if receipt.Score != 100 {
		t.Fatalf("every category at its weight scores %d, want 100", receipt.Score)
	}
	if err := receipt.CheckPass(100); err != nil {
		t.Fatalf("a full score was refused at a pass score of 100: %v", err)
	}
}

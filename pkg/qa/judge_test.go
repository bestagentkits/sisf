package qa_test

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

func fullMarks() qa.Scorecard {
	points := map[qa.Category]int{}
	for category, weight := range qa.DefaultRubric().Weights {
		points[category] = weight
	}
	return qa.Scorecard{Points: points}
}

func TestAScorecardScoresEveryCategoryWithinItsWeight(t *testing.T) {
	t.Parallel()
	rubric := qa.DefaultRubric()
	if err := fullMarks().Validate(rubric); err != nil {
		t.Fatalf("full marks rejected: %v", err)
	}
	cases := map[string]func(*qa.Scorecard){
		"category missing":   func(s *qa.Scorecard) { delete(s.Points, qa.CategorySecurity) },
		"above the weight":   func(s *qa.Scorecard) { s.Points[qa.CategoryExperience] = 16 },
		"negative points":    func(s *qa.Scorecard) { s.Points[qa.CategoryContract] = -1 },
		"unknown category":   func(s *qa.Scorecard) { s.Points["vibes"] = 0 },
		"no points recorded": func(s *qa.Scorecard) { s.Points = nil },
	}
	for name, mutate := range cases {
		card := fullMarks()
		mutate(&card)
		if core.CodeOf(card.Validate(rubric)) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, card.Validate(rubric))
		}
	}
	broken := qa.Rubric{Weights: rubric.Weights, PassScore: 0}
	if fullMarks().Validate(broken) == nil {
		t.Error("a scorecard cannot be valid against a rubric that is not")
	}
}

func TestABaselineIsPinnedAndReadOnly(t *testing.T) {
	t.Parallel()
	valid := qa.Baseline{CommitSHA: sha, PolicySHA: sha, SuiteDigest: digest, WorkspaceID: "qa-ws", ReadOnly: true}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid baseline rejected: %v", err)
	}
	cases := map[string]func(*qa.Baseline){
		"writable":            func(b *qa.Baseline) { b.ReadOnly = false },
		"no workspace":        func(b *qa.Baseline) { b.WorkspaceID = " " },
		"commit not pinned":   func(b *qa.Baseline) { b.CommitSHA = "main" },
		"policy not pinned":   func(b *qa.Baseline) { b.PolicySHA = "" },
		"suite has no digest": func(b *qa.Baseline) { b.SuiteDigest = "latest" },
	}
	for name, mutate := range cases {
		b := valid
		mutate(&b)
		if core.CodeOf(b.Validate()) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, b.Validate())
		}
	}
}

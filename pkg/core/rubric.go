package core

import "slices"

// Names of the scored rubric categories of the review criteria.
const (
	// RubricCorrectness is correctness and regression.
	RubricCorrectness = "correctness_regression"
	// RubricSecurity is security and authentication.
	RubricSecurity = "security_auth"
	// RubricContract is contract and data safety.
	RubricContract = "contract_data_safety"
	// RubricExperience is UX, performance and output.
	RubricExperience = "ux_performance_output"
	// RubricObservability is observability and rollback.
	RubricObservability = "observability_rollback"
)

// RubricCategory is one scored area of the review rubric and the most points
// it can earn.
type RubricCategory struct {
	Name      string `json:"name"`
	MaxPoints int    `json:"max_points"`
}

// rubricCategories is the closed rubric of the review criteria. A receipt
// passes only when it scores exactly these categories, once each, out of
// exactly these points, so a score cannot be reached by inventing a category,
// repeating one or raising its ceiling. Changing a weight is a change to the
// review criteria and needs the Owner.
var rubricCategories = []RubricCategory{
	{RubricCorrectness, 30},
	{RubricSecurity, 25},
	{RubricContract, 20},
	{RubricExperience, 15},
	{RubricObservability, 10},
}

// RubricCategories returns the scored categories of the review rubric with
// their maximum points, which add up to 100. It is the single
// definition both the evaluator and the release step judge against. The
// caller owns the returned slice.
func RubricCategories() []RubricCategory { return slices.Clone(rubricCategories) }

// checkRubric requires the receipt to score every rubric category exactly
// once, out of the category's fixed maximum, and nothing else.
func (r QAReceipt) checkRubric() error {
	scored := map[string]int{}
	for _, c := range r.Checks {
		if c.Kind != CheckRubric {
			continue
		}
		index := slices.IndexFunc(rubricCategories, func(category RubricCategory) bool { return category.Name == c.Name })
		if index < 0 {
			return rubricNotPassed(c.Name, "%q is not a category of the review rubric", c.Name)
		}
		scored[c.Name]++
		switch category := rubricCategories[index]; {
		case scored[c.Name] > 1:
			return rubricNotPassed(c.Name, "rubric category %q is scored more than once", c.Name)
		case c.MaxPoints != category.MaxPoints:
			return rubricNotPassed(c.Name, "rubric category %q is scored out of %d points, the review rubric gives it %d",
				c.Name, c.MaxPoints, category.MaxPoints)
		case c.Points < 0 || c.Points > category.MaxPoints:
			return rubricNotPassed(c.Name, "rubric category %q scores %d outside 0-%d", c.Name, c.Points, category.MaxPoints)
		}
	}
	for _, category := range rubricCategories {
		if scored[category.Name] == 0 {
			return rubricNotPassed(category.Name, "rubric category %q has no score", category.Name)
		}
	}
	return nil
}

// rubricNotPassed builds a refusal that names the rubric category behind it.
func rubricNotPassed(category, format string, args ...any) *Error {
	refusal := notPassed(format, args...)
	refusal.Details = map[string]string{"rubric_category": category}
	return refusal
}

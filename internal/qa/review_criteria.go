package qa

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// rubricLabels maps the category labels of the review criteria table,
// normalized by labelKey, to the rubric categories.
var rubricLabels = map[string]qa.Category{
	labelKey("Correctness and regression"): qa.CategoryCorrectness,
	labelKey("Security and auth"):          qa.CategorySecurity,
	labelKey("Contract and data safety"):   qa.CategoryContract,
	labelKey("UX, performance, output"):    qa.CategoryExperience,
	labelKey("Observability and rollback"): qa.CategoryObservability,
}

var (
	// tableRow matches a two-column Markdown table row ending in a number.
	tableRow = regexp.MustCompile(`^\|\s*([^|]+?)\s*\|\s*(\d+)\s*\|\s*$`)
	// passScoreStatement finds "score of at least 85".
	passScoreStatement = regexp.MustCompile(`(?i)score of at least (\d+)`)
)

// labelKey keeps only the letters of a label, in lower case, so spacing and
// punctuation do not decide which category a row names.
func labelKey(label string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(label) {
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// LoadRubric reads the rubric in force from the review criteria at the
// pinned commit: one weight per category from the rubric table and the pass
// score from the statement "score of at least N". Criteria that are missing,
// ambiguous or that weigh the categories other than the shared rubric put
// QA on POLICY_HOLD: nothing is scored against criteria that cannot be read.
func LoadRubric(ctx context.Context, source policy.Source, reviewSHA core.CommitSHA) (qa.Rubric, error) {
	if source == nil {
		return qa.Rubric{}, core.NewError(core.CodeInvalidArgument, "reading the review criteria needs a source")
	}
	if err := reviewSHA.Validate(); err != nil {
		return qa.Rubric{}, reviewHold("the review criteria are read only at a full commit hash").WithCause(err)
	}
	content, err := source.ReadFile(ctx, reviewSHA, policy.PathReview)
	if err != nil {
		return qa.Rubric{}, reviewHold("%s could not be read at %s", policy.PathReview, reviewSHA).WithCause(err)
	}
	if len(content) > policy.MaxFileSize {
		return qa.Rubric{}, reviewHold("%s is larger than %d bytes", policy.PathReview, policy.MaxFileSize)
	}
	rubric, err := parseRubric(string(content))
	if err != nil {
		return qa.Rubric{}, err
	}
	if err := rubric.Validate(); err != nil {
		return qa.Rubric{}, reviewHold("%s at %s does not state the shared rubric", policy.PathReview, reviewSHA).WithCause(err)
	}
	return rubric, nil
}

// parseRubric extracts the weights and the pass score from the text of the
// review criteria.
func parseRubric(text string) (qa.Rubric, error) {
	rubric := qa.Rubric{Weights: map[qa.Category]int{}}
	for line := range strings.SplitSeq(text, "\n") {
		match := tableRow.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		category, known := rubricLabels[labelKey(match[1])]
		if !known {
			continue
		}
		if _, repeated := rubric.Weights[category]; repeated {
			return qa.Rubric{}, reviewHold("%s weighs %s more than once", policy.PathReview, category)
		}
		weight, err := strconv.Atoi(match[2])
		if err != nil {
			return qa.Rubric{}, reviewHold("%s states a weight for %s that is not a number", policy.PathReview, category)
		}
		rubric.Weights[category] = weight
	}
	statements := passScoreStatement.FindAllStringSubmatch(text, -1)
	if len(statements) == 0 {
		return qa.Rubric{}, reviewHold("%s states no pass score", policy.PathReview)
	}
	for _, statement := range statements {
		score, err := strconv.Atoi(statement[1])
		if err != nil || (rubric.PassScore != 0 && score != rubric.PassScore) {
			return qa.Rubric{}, reviewHold("%s states more than one pass score", policy.PathReview)
		}
		rubric.PassScore = score
	}
	return rubric, nil
}

// reviewHold builds the POLICY_HOLD error for review criteria QA cannot use.
func reviewHold(format string, args ...any) *core.Error {
	return core.NewError(core.CodePolicyHold, fmt.Sprintf(format, args...))
}

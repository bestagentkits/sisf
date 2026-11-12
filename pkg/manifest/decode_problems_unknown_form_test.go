package manifest

import (
	"strings"
	"testing"
)

// A decoder message of no form the rewrite knows may quote a value it never
// classified, so only its line is kept and the value is said to be withheld.
func TestAProblemOfAnUnknownFormWithholdsItsValue(t *testing.T) {
	t.Parallel()
	value := "hunter" + "22"
	for problem, want := range map[string]string{
		"line 7: cannot decode `" + value + "` as anything": "line 7: a value does not fit here (" + withheldValue + ")",
		"something about `" + value + "`":                   "a value does not fit here (" + withheldValue + ")",
	} {
		got := plainProblem(problem)
		if got != want || strings.Contains(got, value) {
			t.Errorf("plainProblem(%q) = %q, want %q", problem, got, want)
		}
	}
}

package decision

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/core"
	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
)

// Identifiers of the typed questions System One answers, as DECISIONS.md
// names them. Uncertainty is answered as a list of named points of doubt,
// not as one option, so it is not one of TypedQuestions.Questions.
const (
	SystemOneVerdict     = "verdict"
	SystemOneKind        = "kind"
	SystemOnePriority    = "priority"
	SystemOneUncertainty = "uncertainty"
)

var systemOneQuestionText = map[string]string{
	SystemOneVerdict:  "Which verdict does the evidence support?",
	SystemOneKind:     "What kind of work would the proposal be?",
	SystemOnePriority: "Which priority does the work deserve?",
}

// TypedQuestions are the System One questions of the decision rules at one
// pinned commit.
type TypedQuestions struct {
	// Revision is the commit DECISIONS.md was read at; empty when parsed
	// from content alone.
	Revision      core.CommitSHA
	SchemaVersion int
	// Questions are the verdict, kind and priority questions, in that order.
	Questions []pkgdecision.Question
}

var (
	// typedSchemaVersion finds each statement of the typed-question schema
	// version, such as "schema version: `1`" or "(schema version 1)".
	typedSchemaVersion = regexp.MustCompile("(?i)schema version[\\s:=`]{0,4}(\\d+)")
	backticked         = regexp.MustCompile("`([^`]+)`")
)

// LoadTypedQuestions reads DECISIONS.md at revision through src, the pinned
// source the policy loader reads, and parses its typed questions. A branch
// name, a missing, unreadable, oversized or malformed file is POLICY_HOLD:
// the questions are never defaulted.
func LoadTypedQuestions(ctx context.Context, src policy.Source, revision core.CommitSHA) (TypedQuestions, error) {
	if src == nil {
		return TypedQuestions{}, hold("unavailable", "the typed questions need a pinned source")
	}
	if err := revision.Validate(); err != nil {
		return TypedQuestions{}, hold("unapproved", "the decision rules are read only at a full commit hash")
	}
	content, err := src.ReadFile(ctx, revision, policy.PathDecisions)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return TypedQuestions{}, hold("missing", fmt.Sprintf("%s does not exist at %s", policy.PathDecisions, revision))
	case err != nil:
		return TypedQuestions{}, hold("unavailable", fmt.Sprintf("%s could not be read at %s", policy.PathDecisions, revision)).WithCause(err)
	case len(content) > policy.MaxFileSize:
		return TypedQuestions{}, hold("invalid", fmt.Sprintf("%s is larger than %d bytes", policy.PathDecisions, policy.MaxFileSize))
	}
	tq, err := ParseTypedQuestions(content)
	if err != nil {
		return TypedQuestions{}, err
	}
	tq.Revision = revision
	return tq, nil
}

// ParseTypedQuestions reads the schema version and the typed-question table
// of DECISIONS.md. Every statement of the version must agree; the table must
// list verdict, kind, priority and uncertainty once each, and every allowed
// value must be one the contract knows. Priority levels may be omitted, in
// which case the contract's levels apply.
func ParseTypedQuestions(content []byte) (TypedQuestions, error) {
	text := string(content)
	version, err := typedQuestionsVersion(text)
	if err != nil {
		return TypedQuestions{}, err
	}
	rows, err := typedQuestionRows(text)
	if err != nil {
		return TypedQuestions{}, err
	}
	known := map[string]func(string) bool{
		SystemOneVerdict:  func(v string) bool { return pkgdecision.Verdict(v).Valid() },
		SystemOneKind:     func(v string) bool { return pkgdecision.Kind(v).Valid() },
		SystemOnePriority: func(v string) bool { return pkgdecision.Priority(v).Valid() },
	}
	tq := TypedQuestions{SchemaVersion: version}
	for _, id := range []string{SystemOneVerdict, SystemOneKind, SystemOnePriority} {
		values := rows[id]
		if len(values) == 0 && id == SystemOnePriority {
			for _, p := range pkgdecision.Priorities() {
				values = append(values, string(p))
			}
		}
		if len(values) == 0 {
			return TypedQuestions{}, hold("invalid", fmt.Sprintf("%s lists no allowed values for %q", policy.PathDecisions, id))
		}
		for i, v := range values {
			if !known[id](v) || slices.Contains(values[:i], v) {
				return TypedQuestions{}, hold("invalid", fmt.Sprintf("%s allows %q for %q, which is unknown or repeated", policy.PathDecisions, v, id))
			}
		}
		tq.Questions = append(tq.Questions, pkgdecision.Question{ID: id, Text: systemOneQuestionText[id], Options: values, Weight: 1})
	}
	return tq, nil
}

// typedQuestionsVersion returns the one schema version the file states.
func typedQuestionsVersion(text string) (int, error) {
	stated := ""
	for _, m := range typedSchemaVersion.FindAllStringSubmatch(text, -1) {
		if stated != "" && m[1] != stated {
			return 0, hold("conflict", policy.PathDecisions+" states more than one typed-question schema version")
		}
		stated = m[1]
	}
	version, err := strconv.Atoi(stated)
	if err != nil || version < 1 {
		return 0, hold("invalid", policy.PathDecisions+" does not state a positive typed-question schema version")
	}
	return version, nil
}

// typedQuestionRows reads the first table after the typed-questions heading
// and returns the backticked allowed values of each question.
func typedQuestionRows(text string) (map[string][]string, error) {
	lines := strings.Split(text, "\n")
	start := slices.IndexFunc(lines, func(l string) bool {
		return strings.HasPrefix(l, "#") && strings.Contains(strings.ToLower(l), "typed questions")
	})
	if start < 0 {
		return nil, hold("invalid", policy.PathDecisions+" has no typed-questions section")
	}
	header := slices.IndexFunc(lines[start:], func(l string) bool {
		l = strings.ToLower(l)
		return strings.HasPrefix(strings.TrimSpace(l), "|") && strings.Contains(l, "question") && strings.Contains(l, "allowed")
	})
	if header < 0 || start+header+2 > len(lines) {
		return nil, hold("invalid", policy.PathDecisions+" has no typed-question table")
	}
	rows := map[string][]string{}
	for _, line := range lines[start+header+2:] {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if !strings.HasPrefix(strings.TrimSpace(line), "|") || len(cells) < 2 {
			break
		}
		id := strings.ToLower(strings.Trim(strings.TrimSpace(cells[0]), "`"))
		if _, seen := rows[id]; seen || (id != SystemOneUncertainty && systemOneQuestionText[id] == "") {
			return nil, hold("invalid", fmt.Sprintf("%s lists the question %q twice or does not know it", policy.PathDecisions, id))
		}
		values := []string{}
		for _, m := range backticked.FindAllStringSubmatch(cells[1], -1) {
			values = append(values, strings.TrimSpace(m[1]))
		}
		rows[id] = values
	}
	for _, id := range []string{SystemOneVerdict, SystemOneKind, SystemOnePriority, SystemOneUncertainty} {
		if _, ok := rows[id]; !ok {
			return nil, hold("invalid", fmt.Sprintf("%s does not list the question %q", policy.PathDecisions, id))
		}
	}
	return rows, nil
}

// hold is POLICY_HOLD with a machine-readable reason, as the policy loader
// reports it.
func hold(reason, message string) *core.Error {
	failure := core.NewError(core.CodePolicyHold, message)
	failure.Details = map[string]string{"reason": reason}
	return failure
}

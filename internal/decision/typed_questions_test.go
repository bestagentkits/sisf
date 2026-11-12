package decision_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/decision"
	"github.com/bestagentkits/sisf/pkg/core"
	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
)

// pinnedFiles serves files at one commit only, like the policy source.
type pinnedFiles struct {
	revision core.CommitSHA
	files    map[string]string
	err      error
}

func (p pinnedFiles) ReadFile(_ context.Context, revision core.CommitSHA, path string) ([]byte, error) {
	if p.err != nil {
		return nil, p.err
	}
	content, ok := p.files[path]
	if revision != p.revision || !ok {
		return nil, core.NewError(core.CodeNotFound, path+" not found")
	}
	return []byte(content), nil
}

func decisionsFile(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../DECISIONS.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestTypedQuestionsAreReadFromTheDecisionRulesAtThePinnedCommit(t *testing.T) {
	t.Parallel()
	pinned := policyRefs().DecisionSHA
	src := pinnedFiles{revision: pinned, files: map[string]string{"DECISIONS.md": decisionsFile(t)}}
	tq, err := decision.LoadTypedQuestions(context.Background(), src, pinned)
	if err != nil {
		t.Fatal(err)
	}
	if tq.Revision != pinned || tq.SchemaVersion != 1 || len(tq.Questions) != 3 {
		t.Fatalf("typed questions = %+v", tq)
	}
	want := map[string][]string{
		decision.SystemOneVerdict:  {"ignore", "observe", "investigate", "propose", "implement", "stop", "collect_more_evidence"},
		decision.SystemOneKind:     {"bug", "existing_improvement", "new_feature", "policy_change"},
		decision.SystemOnePriority: {"low", "medium", "high", "urgent"},
	}
	for _, q := range tq.Questions {
		if !slices.Equal(q.Options, want[q.ID]) || q.Text == "" || q.Weight < 1 {
			t.Errorf("question %s = %+v", q.ID, q)
		}
	}
	other := core.CommitSHA(strings.Repeat("f", 40))
	if _, err := decision.LoadTypedQuestions(context.Background(), src, other); !core.HasCode(err, core.CodePolicyHold) {
		t.Fatalf("another commit has no rules here, so it is a hold: %v", err)
	}
	if _, err := decision.LoadTypedQuestions(context.Background(), src, "main"); !core.HasCode(err, core.CodePolicyHold) {
		t.Fatalf("a branch name is never pinned: %v", err)
	}
}

func TestMissingOrMalformedDecisionRulesAreAPolicyHold(t *testing.T) {
	t.Parallel()
	good := decisionsFile(t)
	cases := map[string]string{
		"empty":                  "",
		"no schema version":      strings.ReplaceAll(strings.ReplaceAll(good, "schema version: `1`", ""), "(schema version 1)", ""),
		"conflicting versions":   strings.Replace(good, "(schema version 1)", "(schema version 2)", 1),
		"no question table":      strings.ReplaceAll(good, "| Question | Allowed values |", "Question and allowed values"),
		"unknown verdict":        strings.Replace(good, "`collect_more_evidence`", "`deploy_now`", 1),
		"unknown kind":           strings.Replace(good, "`policy_change`", "`grant_owner`", 1),
		"verdict without values": strings.Replace(good, "| verdict | `ignore`, `observe`, `investigate`, `propose`, `implement`, `stop`, `collect_more_evidence` |", "| verdict | anything |", 1),
		"unknown question":       strings.Replace(good, "| uncertainty |", "| budget |", 1),
		"duplicated question":    strings.Replace(good, "| uncertainty |", "| kind |", 1),
		"repeated value":         strings.Replace(good, "`ignore`, `observe`", "`ignore`, `ignore`", 1),
	}
	for name, content := range cases {
		if _, err := decision.ParseTypedQuestions([]byte(content)); !core.HasCode(err, core.CodePolicyHold) {
			t.Errorf("%s: want %s, got %v", name, core.CodePolicyHold, err)
		}
	}
	pinned := policyRefs().DecisionSHA
	missing := pinnedFiles{revision: pinned, files: map[string]string{}}
	if _, err := decision.LoadTypedQuestions(context.Background(), missing, pinned); !core.HasCode(err, core.CodePolicyHold) {
		t.Fatalf("a missing file: %v", err)
	}
	broken := pinnedFiles{revision: pinned, err: errBoom}
	if _, err := decision.LoadTypedQuestions(context.Background(), broken, pinned); !core.HasCode(err, core.CodePolicyHold) {
		t.Fatalf("an unreadable source: %v", err)
	}
	huge := pinnedFiles{revision: pinned, files: map[string]string{"DECISIONS.md": good + strings.Repeat(" ", 1<<20)}}
	if _, err := decision.LoadTypedQuestions(context.Background(), huge, pinned); !core.HasCode(err, core.CodePolicyHold) {
		t.Fatalf("an oversized file: %v", err)
	}
}

func TestPriorityLevelsMayBeNarrowedButNotInvented(t *testing.T) {
	t.Parallel()
	good := decisionsFile(t)
	narrowed := strings.Replace(good, "| priority | ordered levels set by the deployment |", "| priority | `medium`, `high` |", 1)
	tq, err := decision.ParseTypedQuestions([]byte(narrowed))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range tq.Questions {
		if q.ID == decision.SystemOnePriority && !slices.Equal(q.Options, []string{string(pkgdecision.PriorityMedium), string(pkgdecision.PriorityHigh)}) {
			t.Fatalf("priority options = %v", q.Options)
		}
	}
	invented := strings.Replace(good, "| priority | ordered levels set by the deployment |", "| priority | `critical` |", 1)
	if _, err := decision.ParseTypedQuestions([]byte(invented)); !core.HasCode(err, core.CodePolicyHold) {
		t.Fatalf("an unknown priority level: %v", err)
	}
}

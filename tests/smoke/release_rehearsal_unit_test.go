//go:build smoke

package smoke

import (
	"bufio"
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/internal/qa/agentjudge"
	"github.com/bestagentkits/sisf/pkg/decision"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/qa"
)

func TestTheRunModeComesFromTheEnvironment(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		env  map[string]string
		want runMode
		bad  bool
	}{
		"paid":      {env: nil, want: modePaid},
		"up only":   {env: map[string]string{upOnlyEnv: "1"}, want: modeUpOnly},
		"rehearsal": {env: map[string]string{rehearsalEnv: "1"}, want: modeRehearsal},
		"both":      {env: map[string]string{upOnlyEnv: "1", rehearsalEnv: "1"}, bad: true},
	} {
		got, err := runModeFrom(func(k string) string { return tc.env[k] })
		if (err != nil) != tc.bad || (!tc.bad && got != tc.want) {
			t.Errorf("%s: mode %v, error %v", name, got, err)
		}
	}
	if modeRehearsal.spends() || modeUpOnly.spends() || !modePaid.spends() {
		t.Error("only the paid run spends")
	}
	paths := map[string]bool{reportPath(modePaid): true, reportPath(modeUpOnly): true, reportPath(modeRehearsal): true}
	if len(paths) != 3 || reportPath(modeRehearsal) != rehearsalReportRelPath {
		t.Errorf("the reports of the three runs share a file: %v", paths)
	}
	if !strings.Contains(modeRehearsal.title(), "not the paid smoke") {
		t.Errorf("the rehearsal's report is titled %q", modeRehearsal.title())
	}
}

// The rehearsal's install runs no paid model: no real coding agent, no
// model names, no operator home, and the QA agent only through the fake
// launcher.
func TestTheRehearsalsInitLineStartsNoPaidAgent(t *testing.T) {
	t.Parallel()
	c := standInChoices()
	args := rehearsalInitArgs("/srv/smoke/factory", c)
	line := strings.Join(args, " ")
	for _, banned := range []string{"--coder-agent", "--coder-model", "--qa-model", "--agent-home", "--qa-home", "--workspace-source", "claude"} {
		if strings.Contains(line, banned) {
			t.Errorf("the rehearsal's init line carries %s: %s", banned, line)
		}
	}
	for flag, want := range map[string]string{"--qa-judge": "agent", "--qa-agent-binary": c.sandbox.qaLauncher, "--exec": "docker", "--repo": sandboxRepo} {
		if got, ok := flagValue(args, flag); !ok || got != want {
			t.Errorf("%s = %q (%v), want %q", flag, got, ok, want)
		}
	}
	if !slices.Contains(args, "--dev") {
		t.Error("the rehearsal is not a development install")
	}
}

// The fake judge's answer is what the codex adapter reads and the agent
// judge accepts: one judgment giving every criterion its full points.
func TestTheFakeQAJudgeAnswersWithAFullValidJudgment(t *testing.T) {
	t.Parallel()
	lines, err := fakeJudgment()
	if err != nil {
		t.Fatal(err)
	}
	var last string
	sc := bufio.NewScanner(bytes.NewReader(lines))
	sc.Buffer(nil, 1<<20)
	n := 0
	for sc.Scan() {
		n++
		var e struct {
			Type string
			Item struct{ Type, Text string }
		}
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("line %d is not JSON: %v", n, err)
		}
		if e.Type == "item.completed" && e.Item.Type == "agent_message" {
			last = e.Item.Text
		}
	}
	rubric := qa.DefaultRubric()
	j, err := agentjudge.ParseJudgment(last, rubric)
	if err != nil {
		t.Fatalf("the fake judgment is refused: %v", err)
	}
	total := 0
	for category, weight := range rubric.Weights {
		if j.Scores[category].Points != weight {
			t.Errorf("%s scored %d of %d", category, j.Scores[category].Points, weight)
		}
		total += j.Scores[category].Points
	}
	if total != 100 || !strings.Contains(string(lines), `"type":"turn.completed"`) {
		t.Errorf("total %d; lines:\n%s", total, lines)
	}
}

// The rehearsal's issue goes straight to Ready through intake, like the
// paid run's, and its patch adds a new note on a path no run used, outside
// every protected path, and asks for nothing the paid run asks for.
func TestTheRehearsalsIssueAndPatchStayOffThePaidRunsTask(t *testing.T) {
	t.Parallel()
	r := newRehearsal(time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC))
	var labels []github.Label
	for _, name := range issueLabels {
		labels = append(labels, github.Label{Name: name})
	}
	read := intake.ReadLabels(labels, intake.LabelRules{})
	draft := intake.Analyze(github.Issue{Title: r.issueTitle(), Body: r.issueBody(), Labels: labels,
		Author: github.User{ID: 1, Login: "operator"}, AuthorAssociation: github.AssociationMember}, nil, read)
	if !draft.Complete() {
		t.Fatalf("intake would ask for %v", draft.Missing)
	}
	if verdict := intake.Classify(draft, github.AssociationMember, sandboxCatalog); verdict.NeedsApproval || verdict.Kind != decision.KindExistingImprovement {
		t.Errorf("the rehearsal's issue needs the Owner: %+v", verdict)
	}
	patch := r.patch()
	if !strings.HasPrefix(patch, "diff --git a/"+r.note+" b/"+r.note+"\nnew file mode 100644\n--- /dev/null\n+++ b/"+r.note+"\n@@ -0,0 +1,3 @@\n") {
		t.Errorf("the patch:\n%s", patch)
	}
	if _, protected := stages.ProtectedPath(r.note); protected || strings.Contains(patch+r.issueBody(), versionRoute) {
		t.Errorf("the rehearsal touches a protected path or the paid run's task: %s", r.note)
	}
	if newRehearsal(time.Date(2026, 11, 11, 20, 0, 1, 0, time.UTC)).note == r.note {
		t.Error("two rehearsals a second apart write the same note")
	}
}

//go:build smoke

package smoke

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
	good, _ := newRehearsal(time.Now(), "")
	bad, _ := newRehearsal(time.Now(), "bad")
	paths := map[string]bool{reportPath(modePaid): true, reportPath(modeUpOnly): true, good.reportPath(): true, bad.reportPath(): true}
	if len(paths) != 4 || reportPath(modeRehearsal) != good.reportPath() {
		t.Errorf("the reports of the four runs share a file: %v", paths)
	}
	if _, err := newRehearsal(time.Now(), "worse"); err == nil {
		t.Error("a candidate that is neither good nor bad was accepted")
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

// The issue of either rehearsal goes straight to Ready through intake,
// like the paid run's, touches no protected path and asks for nothing the
// paid run asks for.
func TestTheRehearsalsIssuesNeedNoOwnerAndStayOffThePaidRunsTask(t *testing.T) {
	t.Parallel()
	var labels []github.Label
	for _, name := range issueLabels {
		labels = append(labels, github.Label{Name: name})
	}
	read := intake.ReadLabels(labels, intake.LabelRules{})
	for _, candidate := range []string{"good", "bad"} {
		r, err := newRehearsal(time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC), candidate)
		if err != nil {
			t.Fatal(err)
		}
		draft := intake.Analyze(github.Issue{Title: r.issueTitle(), Body: r.issueBody(), Labels: labels,
			Author: github.User{ID: 1, Login: "operator"}, AuthorAssociation: github.AssociationMember}, nil, read)
		if !draft.Complete() {
			t.Fatalf("%s: intake would ask for %v", candidate, draft.Missing)
		}
		if verdict := intake.Classify(draft, github.AssociationMember, sandboxCatalog); verdict.NeedsApproval || verdict.Kind != decision.KindExistingImprovement {
			t.Errorf("%s: the rehearsal's issue needs the Owner: %+v", candidate, verdict)
		}
		if strings.Contains(r.issueBody(), versionRoute) {
			t.Errorf("%s: the rehearsal asks for the paid run's task", candidate)
		}
	}
	for _, path := range []string{rehearsalPage, rehearsalFaultFile} {
		if _, protected := stages.ProtectedPath(path); protected {
			t.Errorf("the rehearsal touches the protected path %s", path)
		}
	}
}

// rehearsedPage is the application's page as the sandbox holds it.
const rehearsedPage = "package main\n\nfunc page() {\n\tn := 1\n" +
	"\t_, _ = fmt.Fprintf(w, \"Hello from the sample app: visit %d\\n\", n)\n}\n"

// applyPatch applies patch with git to a directory holding files and
// returns the directory.
func applyPatch(t *testing.T, files map[string]string, patch string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("git", "apply", "-")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(patch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git apply: %v\n%s\nthe patch:\n%s", err, out, patch)
	}
	return dir
}

// The good candidate's patch is one git applies to the page and that
// changes its greeting, also after an earlier rehearsal changed it; two
// rehearsals a second apart ask for different greetings.
func TestTheGoodCandidatesPatchChangesTheGreetingOfThePage(t *testing.T) {
	t.Parallel()
	r, _ := newRehearsal(time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC), "good")
	page := rehearsedPage
	for _, reh := range []rehearsal{r, {stamp: "20261111-200001"}} {
		patch, err := reh.patch(page)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(applyPatch(t, map[string]string{rehearsalPage: page}, patch), rehearsalPage))
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Replace(rehearsedPage, "Hello from the sample app", reh.greeting(), 1)
		if string(raw) != want || !addsGreeting(patch, reh.greeting()) {
			t.Errorf("the patched page:\n%s\nwant:\n%s", raw, want)
		}
		page = string(raw)
	}
	if r.greeting() == (rehearsal{stamp: "20261111-200001"}).greeting() {
		t.Error("two rehearsals a second apart ask for the same greeting")
	}
	for name, page := range map[string]string{
		"no greeting":   "package main\n\nfunc page() {}\n",
		"two greetings": rehearsedPage + strings.SplitAfter(rehearsedPage, "\n")[4] + "\n",
	} {
		if patch, err := r.patch(page); err == nil {
			t.Errorf("a page with %s was patched:\n%s", name, patch)
		}
	}
}

// The bad candidate's patch adds one file, whatever the page says, that
// git applies and that turns the page's injected failures on for every
// request.
func TestTheBadCandidatesPatchAddsAFileThatFailsEveryPage(t *testing.T) {
	t.Parallel()
	r, _ := newRehearsal(time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC), "bad")
	patch, err := r.patch("")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(applyPatch(t, nil, patch), rehearsalFaultFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "package main\n") || !strings.Contains(string(raw), `os.Setenv("HELLO_FAULT_ERROR_PERCENT", "100")`) {
		t.Errorf("the file the bad candidate adds:\n%s", raw)
	}
}

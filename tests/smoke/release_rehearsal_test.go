//go:build smoke

package smoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/internal/qa/agentjudge"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// The release rehearsal is the run that spends nothing but still takes one
// issue the whole way against the real sandbox: intake, the scripted coding
// agent leaving a deterministic patch, a real candidate build in the build
// sandbox, a fake QA judge, the real release pipeline with its default
// windows and holds, and the candidate's own program deployed by the local
// backend. It is not the paid smoke: no claude or codex is started, and its
// report has a file of its own, so the release, merge and rollback paths
// are proven live before any money is spent.
//
// It rehearses one of two candidates (SISF_SMOKE_REHEARSAL_CANDIDATE):
//
//   - good, the default, changes the greeting of the application's page.
//     The changed page must be served through the edge under the
//     candidate's digest while its pull request is still open; the issue
//     must then close and the pull request merge; and the promoted program
//     must still serve after a restart of the units and after `sisf down`
//     and `sisf up`.
//   - bad adds a file that makes every page request fail while the health
//     check still passes. It builds and the fake judge passes it, the
//     factory's synthetic suite fails it during the hold, and the release
//     must be rolled back to the last known good release, the pull request
//     left unmerged and the issue left Blocked with the reason.
//
//   - The coder is the scripted agent every Standalone install has when no
//     real one is chosen; the run writes its patch into the install before
//     `sisf up`.
//   - The QA judge is the agent judge, as in the paid run, but its launcher
//     (--qa-agent-binary) is a script that reads the prompt and answers
//     with a fixed judgment giving every criterion its full points. The
//     hard gates are still the supervisor's own.
//   - No traffic is sent, so the release is judged the way the paid run's
//     will be: by the low-traffic hold.

// rehearsalEnv switches the run to the rehearsal; rehearsalCandidateEnv
// picks its candidate, good when unset.
const (
	rehearsalEnv          = "SISF_SMOKE_REHEARSAL"
	rehearsalCandidateEnv = "SISF_SMOKE_REHEARSAL_CANDIDATE"
)

// runMode is what one run of TestRealLifecycleSmoke does.
type runMode int

const (
	// modePaid is the real smoke: claude codes, codex judges.
	modePaid runMode = iota
	// modeUpOnly stops once the factory is up and the operator signed in.
	modeUpOnly
	// modeRehearsal takes an issue to Closed with a scripted coder and a
	// fake QA judge.
	modeRehearsal
)

// runModeFrom reads the run's mode from the environment.
func runModeFrom(getenv func(string) string) (runMode, error) {
	upOnly, rehearsal := getenv(upOnlyEnv) == "1", getenv(rehearsalEnv) == "1"
	switch {
	case upOnly && rehearsal:
		return 0, errors.New(upOnlyEnv + " and " + rehearsalEnv + " are different runs; set one")
	case upOnly:
		return modeUpOnly, nil
	case rehearsal:
		return modeRehearsal, nil
	}
	return modePaid, nil
}

// spends reports whether the mode runs paid models.
func (m runMode) spends() bool { return m == modePaid }

// title is the heading of the mode's report.
func (m runMode) title() string {
	if m == modeRehearsal {
		return "Release rehearsal report (scripted coder and fake QA judge; not the paid smoke run)"
	}
	return "Real smoke run report"
}

// rehearsalReportRelPath is where the rehearsal's report goes, so it never
// overwrites the report of a paid run; the rehearsal of a rollback has a
// file of its own.
const (
	rehearsalReportRelPath         = "plans/261110-1325-sisf-p0-p3-implementation/reports/release-rehearsal-report.md"
	rollbackRehearsalReportRelPath = "plans/261110-1325-sisf-p0-p3-implementation/reports/release-rollback-rehearsal-report.md"
)

// rehearsalPage is the file of the application the good candidate changes,
// and rehearsalFaultFile the file the bad candidate adds.
const (
	rehearsalPage      = "app.go"
	rehearsalFaultFile = "release_rehearsal_fault.go"
)

// rehearsal is the one issue of a rehearsal and the candidate it asks for.
type rehearsal struct {
	stamp string
	// bad makes the candidate the one whose release must be rolled back.
	bad bool
}

// newRehearsal reads which candidate the rehearsal is of.
func newRehearsal(now time.Time, candidate string) (rehearsal, error) {
	r := rehearsal{stamp: now.UTC().Format("20060102-150405")}
	switch candidate {
	case "", "good":
	case "bad":
		r.bad = true
	default:
		return r, errors.New(rehearsalCandidateEnv + " must be good or bad")
	}
	return r, nil
}

// greeting is what the good candidate's page says before the visit number.
func (r rehearsal) greeting() string { return "Hello from release rehearsal " + r.stamp }

// reportPath is where the rehearsal's report goes: each candidate has a
// file of its own.
func (r rehearsal) reportPath() string {
	if r.bad {
		return rollbackRehearsalReportRelPath
	}
	return rehearsalReportRelPath
}

// issueTitle and issueBody carry the sections intake reads, as the paid
// run's issue does, for a change that touches no foundation file. The
// affected feature is one FEATURES.md of the sandbox lists.
func (r rehearsal) issueTitle() string {
	if r.bad {
		return "release rehearsal " + r.stamp + ": a candidate that must be rolled back"
	}
	return "release rehearsal " + r.stamp + ": change the page's greeting"
}

func (r rehearsal) issueBody() string {
	if r.bad {
		return `## Scope

Add ` + "`" + rehearsalFaultFile + "`" + `, which turns the application's injected page failures on
for every request. This issue is a release rehearsal of the factory that
must end in a rollback: a scripted agent writes the change and a fake judge
scores it; no model runs. The factory's synthetic suite must fail the
release and the last known good release must be restored.

## Affected features

- ` + rollbackRehearsalFeature + `

## Definition of Done

- ` + "`" + rehearsalFaultFile + "`" + ` exists.
- ` + "`go test ./...`" + ` passes.
`
	}
	return `## Scope

Change the greeting of the page ` + "`GET /`" + ` in ` + "`" + rehearsalPage + "`" + ` to "` + r.greeting() + `". This
issue is a release rehearsal of the factory: a scripted agent writes the
change and a fake judge scores it; no model runs.

## Affected features

- ` + rehearsalFeature + `

## Definition of Done

- ` + "`GET /`" + ` answers "` + r.greeting() + `: visit N".
- ` + "`go test ./...`" + ` passes.
`
}

// rehearsalFeature is the sandbox feature the good candidate changes, and
// rollbackRehearsalFeature the one the bad candidate turns on.
const (
	rehearsalFeature         = "Visit page"
	rollbackRehearsalFeature = "Fault injection"
)

// greetingLine is the line of the page that greets a visitor, whatever an
// earlier rehearsal made it say.
var greetingLine = regexp.MustCompile(`^\t_, _ = fmt\.Fprintf\(w, "Hello from [^"\\]*: visit %d\\n", n\)$`)

// patch is the scripted agent's patch against page, the content of
// rehearsalPage at the commit the policy is approved at. The good
// candidate replaces the one line that greets a visitor; the bad one adds a
// file, so it applies to any head.
func (r rehearsal) patch(page string) (string, error) {
	if r.bad {
		lines := []string{
			"package main", "", `import "os"`, "",
			"// Added by the scripted coding agent of release rehearsal " + r.stamp + ", whose",
			"// release must be rolled back: every page request fails, so the factory's",
			"// synthetic suite fails while the health check still passes.",
			"func init() {", "\t_ = os.Setenv(\"HELLO_FAULT_ERROR_PERCENT\", \"100\")", "}",
		}
		var b strings.Builder
		fmt.Fprintf(&b, "diff --git a/%[1]s b/%[1]s\nnew file mode 100644\n--- /dev/null\n+++ b/%[1]s\n@@ -0,0 +1,%[2]d @@\n", rehearsalFaultFile, len(lines))
		for _, l := range lines {
			b.WriteString("+" + l + "\n")
		}
		return b.String(), nil
	}
	lines := strings.Split(page, "\n")
	at := -1
	for i, l := range lines {
		if greetingLine.MatchString(l) {
			if at >= 0 {
				return "", errors.New(rehearsalPage + " greets a visitor on more than one line")
			}
			at = i
		}
	}
	if at < 1 || at+1 >= len(lines) {
		return "", errors.New(rehearsalPage + " has no line that greets a visitor as the rehearsal expects it")
	}
	changed := "\t_, _ = fmt.Fprintf(w, \"" + r.greeting() + ": visit %d\\n\", n)"
	return fmt.Sprintf("diff --git a/%[1]s b/%[1]s\n--- a/%[1]s\n+++ b/%[1]s\n@@ -%[2]d,3 +%[2]d,3 @@\n %[3]s\n-%[4]s\n+%[5]s\n %[6]s\n",
		rehearsalPage, at, lines[at-1], lines[at], changed, lines[at+1]), nil
}

// writePatch puts the patch where the install's scripted agent reads it;
// `sisf up` keeps a patch that is already there.
func (r rehearsal) writePatch(run *commandRunner, home, patch string) {
	run.t.Helper()
	dir := install.Layout{Dir: home}.SimAgentDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		run.fatalf("the scripted agent's directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "patch.diff"), []byte(patch), 0o644); err != nil {
		run.fatalf("the scripted agent's patch: %v", err)
	}
}

// sandboxFile reads a file of the sandbox at a commit.
func sandboxFile(ctx context.Context, r *commandRunner, path, commit string) (string, error) {
	return r.run(ctx, ghLimit, nil, "gh", "api", "-H", "Accept: application/vnd.github.raw",
		"repos/"+sandboxRepo+"/contents/"+path+"?ref="+commit)
}

// rehearsalInitArgs creates the rehearsal's install: as the paid run's,
// but with the scripted coding agent (no --coder-agent, so no model, no
// operator home and no workspace clone) and the fake QA launcher in place
// of the one that runs codex.
func rehearsalInitArgs(dir string, c runChoices) []string {
	return []string{
		"init", "--dev", "--mode", "standalone", "--dir", dir, "--repo", sandboxRepo,
		"--github-credential", "gh-cli",
		"--budget-cap", budgetCapUnits, "--alert-threshold", alertThresholdUnits,
		"--qa-judge", "agent", "--qa-agent", "codex",
		"--exec", "docker", "--exec-docker-endpoint", c.sandbox.endpoint, "--exec-docker-network", c.sandbox.network,
		"--egress-proxy", c.sandbox.egressProxy, "--runner-image", c.sandbox.runnerImage,
		"--qa-agent-binary", c.sandbox.qaLauncher, "--build-command", c.buildCommand,
		"--github-reconcile-interval", reconcileInterval,
	}
}

// fakeJudgment is the fake QA judge's answer, as codex's JSON lines: the
// prompt is not read beyond its end, and the final message holds one
// judgment giving every criterion of the default rubric its full points.
func fakeJudgment() ([]byte, error) {
	scores := map[string]any{}
	for category, weight := range qa.DefaultRubric().Weights {
		scores[string(category)] = map[string]any{"points": weight, "evidence": "release rehearsal: a fixed judgment, no model read the diff"}
	}
	judgment, err := json.Marshal(map[string]any{"version": agentjudge.JudgmentVersion, "scores": scores,
		"hard_gate_observations": []any{}, "summary": "Release rehearsal: the fake QA judge scores every candidate in full; the supervisor's hard gates still decide."})
	if err != nil {
		return nil, err
	}
	message := agentjudge.OpenMarker + "\n" + string(judgment) + "\n" + agentjudge.CloseMarker
	var out []byte
	for _, event := range []any{
		map[string]any{"type": "thread.started", "thread_id": "release-rehearsal"},
		map[string]any{"type": "turn.started"},
		map[string]any{"type": "item.completed", "item": map[string]any{"id": "item_0", "type": "agent_message", "text": message}},
		map[string]any{"type": "turn.completed", "usage": map[string]any{"input_tokens": 0, "cached_input_tokens": 0, "output_tokens": 0}},
	} {
		line, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		out = append(append(out, line...), '\n')
	}
	return out, nil
}

// writeFakeQALauncher writes the fake QA launcher: it reads the prompt
// and prints the fixed judgment. It starts no program besides cat.
func writeFakeQALauncher(run *commandRunner, path string) {
	run.t.Helper()
	lines, err := fakeJudgment()
	if err != nil {
		run.fatalf("the fake judgment: %v", err)
	}
	answer := path + ".jsonl"
	if strings.ContainsAny(answer, "'\n") {
		run.fatalf("%q cannot be written into the launcher", answer)
	}
	body := "#!/bin/sh\n# The fake QA judge of a release rehearsal: it reads the prompt and\n" +
		"# answers with a fixed judgment. No model runs.\ncat >/dev/null\nexec cat '" + answer + "'\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		run.fatalf("the fake QA launcher: %v", err)
	}
	if err := os.WriteFile(answer, lines, 0o600); err != nil {
		run.fatalf("the fake judgment: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		run.fatalf("the fake QA launcher: %v", err)
	}
}

// greetingOnDefaultBranch reports whether commit is on the sandbox's default
// branch and changes the page to the rehearsal's greeting.
func greetingOnDefaultBranch(ctx context.Context, r *commandRunner, commit, greeting string) (bool, string, error) {
	if len(commit) != 40 {
		return false, "", fmt.Errorf("%q is not a commit id", commit)
	}
	branch, err := r.run(ctx, ghLimit, nil, "gh", "api", "repos/"+sandboxRepo, "--jq", ".default_branch")
	if err != nil {
		return false, "", err
	}
	branch = strings.TrimSpace(branch)
	status, err := r.run(ctx, ghLimit, nil, "gh", "api", "repos/"+sandboxRepo+"/compare/"+commit+"..."+branch, "--jq", ".status")
	if err != nil {
		return false, "", err
	}
	status = strings.TrimSpace(status)
	patch, err := r.run(ctx, ghLimit, nil, "gh", "api", "repos/"+sandboxRepo+"/commits/"+commit,
		"--jq", `.files[] | select(.filename == "`+rehearsalPage+`") | .patch // ""`)
	if err != nil {
		return false, "", err
	}
	adds := addsGreeting(patch, greeting)
	evidence := fmt.Sprintf("%s against %s: %s; its diff of %s adds %q: %t", commit, branch, status, rehearsalPage, greeting, adds)
	return (status == "identical" || status == "ahead") && adds, evidence, nil
}

// addsGreeting reports whether a unified diff adds a line with greeting.
func addsGreeting(patch, greeting string) bool {
	for line := range strings.SplitSeq(patch, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") && strings.Contains(line, greeting) {
			return true
		}
	}
	return false
}

// paidAgentConfigured reports whether the install would start a paid
// coding agent, or a QA launcher other than the rehearsal's fake one, from
// what its factory.yaml records.
func paidAgentConfigured(home string) (bool, string) {
	m, err := install.Layout{Dir: home}.LoadManifest()
	if err != nil {
		return true, "the install's factory.yaml cannot be read: " + err.Error()
	}
	coder, _ := m.Extensions["coder_agent"].(string)
	launcher, _ := m.Extensions["qa_agent_binary"].(string)
	fake := false
	if raw, err := os.ReadFile(launcher); err == nil {
		fake = strings.Contains(string(raw), "fake QA judge of a release rehearsal")
	}
	evidence := fmt.Sprintf("coder %q (empty or fake is the scripted agent), QA launcher is the rehearsal's fake judge: %t", coder, fake)
	return (coder != "" && coder != "fake") || !fake, evidence
}

//go:build smoke

package smoke

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/pkg/core"
)

// TestRealLifecycleSmoke is the one real run of the factory: a Standalone
// install on SQLite against the private sandbox repository, claude as the
// coder, candidates built in a Docker build sandbox the run provides,
// codex as the independent QA judge in a container, a capped budget, and
// one issue taken from intake to closed. It costs real model quota and writes
// to GitHub, so it runs only under the smoke tag (`make smoke`), only with
// SISF_SMOKE_REPO=bestagentkits/sisf-sandbox, and only after a preflight
// that changes nothing. The report is written whatever the outcome.
//
// With SISF_SMOKE_UP_ONLY=1 the run spends nothing: it stops once the
// factory is up, doctor passes and the operator is signed in, before any
// policy is approved, any issue is opened or any agent is given a task.
//
// With SISF_SMOKE_REHEARSAL=1 the run spends nothing either but goes the
// whole way: a scripted coder and a fake QA judge take one issue through
// the real build sandbox and release, to merge and close or, with
// SISF_SMOKE_REHEARSAL_CANDIDATE=bad, to a rollback (release_rehearsal_test.go).
func TestRealLifecycleSmoke(t *testing.T) {
	mode, err := runModeFrom(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	upOnly := mode == modeUpOnly
	reh, err := newRehearsal(time.Now(), os.Getenv(rehearsalCandidateEnv))
	if err != nil {
		t.Fatal(err)
	}
	// The temporary directory is created first so that it is removed last,
	// after the teardown and the report have read the install's logs.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, upLog := filepath.Join(root, "factory"), filepath.Join(root, "up.log")
	rep := &smokeReport{Title: mode.title(), Started: time.Now(), Repo: os.Getenv(repoEnv)}
	scrub := newSecretScrubber(os.Environ())
	// The operator's home names the person; the report is public.
	if h, err := os.UserHomeDir(); err == nil && filepath.IsAbs(h) && h != "/" {
		scrub.addPersonal(h, "~")
	}
	r := &commandRunner{t: t, dir: repoRoot(), scrub: scrub, report: rep}
	stage := "preflight (no side effect)"
	t.Cleanup(func() {
		rep.Finished = time.Now()
		rep.Outcome = reportOutcome(t.Failed(), stage, rep.Checks)
		if rep.Outcome != "PASS" {
			evidence := failureEvidence(upLog, home, scrub)
			for _, e := range evidence {
				t.Log(e)
			}
			rep.Evidence = append(rep.Evidence, evidence...)
		}
		if !t.Failed() && rep.Outcome != "PASS" {
			// A failed check the run went on after still fails the run.
			t.Errorf("the run listed failed checks: %s", rep.Outcome)
		}
		switch mode {
		case modeUpOnly:
			rep.Outcome += " (up only: nothing was spent)"
		case modeRehearsal:
			rep.Outcome += " (release rehearsal: no model ran, nothing was spent)"
		}
		rel := reportPath(mode)
		if mode == modeRehearsal {
			rel = reh.reportPath()
		}
		path := filepath.Join(repoRoot(), rel)
		if err := writeReport(path, rep, scrub); err != nil {
			t.Errorf("the report could not be written to %s: %v", path, err)
			return
		}
		t.Logf("report written to %s", path)
	})
	ctx := t.Context()

	started := time.Now()
	if err := checkSandboxRepo(os.Getenv(repoEnv)); err != nil {
		r.fatalf("%v", err)
	}
	facts := preflight(ctx, r, mode)
	if !upOnly {
		requireIdleSandbox(ctx, r)
	}
	choices := choicesFrom(os.Getenv, filepath.Join(root, "sandbox"))
	if mode != modeRehearsal {
		requireOutsideHome(r, home, choices.operatorHome)
	}
	bins := buildBinaries(ctx, r, filepath.Join(root, "bin"))
	probeFlags(ctx, r, flagProbes(bins, home, repoRoot(), choices))
	// The coding agent's workspaces are checked out from a local clone of
	// the private sandbox, made with the operator's gh login; no credential
	// is written into the install. The scripted coder of the rehearsal
	// needs none.
	if mode != modeRehearsal {
		if _, err := r.run(ctx, 5*time.Minute, nil, "gh", "repo", "clone", sandboxRepo, choices.workspaceSource); err != nil {
			r.fatalf("clone the sandbox for the coding agent's workspaces: %v", err)
		}
	}
	rep.phase("preflight and build", started)

	// The build sandbox spends nothing; a sandbox that cannot build the
	// sandbox's head stops the run before the factory starts.
	stage, started = "build sandbox", time.Now()
	sb := newBuildSandbox(r, root)
	sb.fakeQA = mode == modeRehearsal
	codex := ""
	if !sb.fakeQA {
		codex = codexVersion(ctx, r)
	}
	sb.provision(ctx, r, "https://github.com/"+sandboxRepo+".git", codex)
	sb.checkIsolation(ctx, r)
	choices.sandbox = sb.choice()
	rep.phase("build sandbox", started)

	stage, started = "install", time.Now()
	env := sisfEnv("SISF_HOME=" + home)
	initLine := initArgs(home, choices)
	if mode == modeRehearsal {
		initLine = rehearsalInitArgs(home, choices)
	}
	out, err := r.run(ctx, 2*time.Minute, env, bins.sisf, initLine...)
	seedInstallSecrets(r, home)
	if err != nil {
		r.fatalf("sisf init: %v", err)
	}
	r.logf("sisf init:\n%s", out)
	if err := declareSandboxInputs(home); err != nil {
		r.fatalf("declare the sandbox application's inputs: %v", err)
	}
	factoryID := installedFactoryID(r, home)
	sb.startFilter(ctx, r, bins.sisf, factoryID, filepath.Join(root, "docker-filter.log"))
	head, err := defaultBranchRevision(ctx, r)
	if err != nil {
		r.fatalf("the sandbox's head commit cannot be read: %v", err)
	}
	if mode == modeRehearsal {
		page, err := sandboxFile(ctx, r, rehearsalPage, head)
		if err != nil {
			r.fatalf("the sandbox's %s cannot be read: %v", rehearsalPage, err)
		}
		patch, err := reh.patch(page)
		if err != nil {
			r.fatalf("the rehearsal's patch: %v", err)
		}
		reh.writePatch(r, home, patch)
	}
	sb.selfBuild(ctx, r, factoryID, core.CommitSHA(head))
	// What a rehearsal leaves open in the sandbox is closed after the
	// factory stopped, whatever the outcome.
	number := 0
	t.Cleanup(func() {
		if mode == modeRehearsal && number != 0 {
			closeRehearsalWork(r, number, rep.PullRequests)
		}
	})
	proc, err := startFactory(r, bins.sisf, upArgs(home, repoRoot()), env, upLog)
	if err != nil {
		r.fatalf("start sisf up: %v", err)
	}
	t.Cleanup(func() {
		began := time.Now()
		rep.Teardown = scrub.clean(proc.stop(r, bins.sisf, home, env))
		rep.phase("teardown", began)
	})
	if err := proc.waitUntilUp(ctx); err != nil {
		r.fatalf("%v", err)
	}
	seedInstallSecrets(r, home) // `sisf up` may have written more
	if out, err := r.run(ctx, 2*time.Minute, env, bins.sisf, "doctor", "--dir", home); err != nil {
		r.fatalf("sisf doctor on the running factory: %v", err)
	} else {
		r.logf("sisf doctor:\n%s", out)
		rep.check("sisf doctor", statusPass, scrub.clean(lastLines(out, 5)))
	}
	if upOnly {
		upOnlySignIn(ctx, r, bins.sisf, home)
		rep.phase("install, up, doctor and sign-in", started)
		return
	}
	approvePolicy(ctx, r, home)
	installed, err := installedAppDigest(home)
	if err != nil {
		r.fatalf("the installed application's digest cannot be read: %v", err)
	}
	watch := &candidateWatch{home: home, installed: installed}
	obs, err := newFactoryObserver(r, bins.sisf, home)
	if err != nil {
		r.fatalf("%v", err)
	}
	obs.signIn(ctx)
	rep.phase("install, up, doctor and policy", started)

	stage, started = "run", time.Now()
	title, body := issueTitle, issueBody
	if mode == modeRehearsal {
		title, body = reh.issueTitle(), reh.issueBody()
	}
	opened, url, err := openIssue(ctx, r, title, body)
	if err != nil {
		r.fatalf("open the issue: %v", err)
	}
	number = opened
	rep.IssueURL = url
	issue := sandboxRepo + "#" + strconv.Itoa(number)
	r.logf("opened %s", url)
	rollback := mode == modeRehearsal && reh.bad
	runErr := waitForClose(ctx, r, obs, proc, watch, issue, number, rollback)
	if rollback {
		rep.phase("run (issue opened to rolled back and blocked)", started)
	} else {
		rep.phase("run (issue opened to closed)", started)
	}
	if runErr != nil {
		r.errorf("%v", runErr)
	}

	// The checks run even after a failed run: their evidence goes in the report.
	stage, started = "assertions", time.Now()
	var rehearsed *rehearsal
	if mode == modeRehearsal {
		rehearsed = &reh
	}
	if rollback {
		assertRolledBack(ctx, r, obs, watch, facts.login, issue, number, rep.Started)
	} else {
		assertOutcome(ctx, r, obs, watch, facts.login, issue, number, rep.Started, rehearsed)
	}
	closeReleaseRecords(ctx, r, rep.RunIDs)
	rep.phase("assertions", started)
	if rehearsed != nil && !rollback && !t.Failed() {
		stage, started = "restarts", time.Now()
		v, _ := obs.inspect(ctx, issue)
		proc = assertPromotedSurvivesRestarts(ctx, r, proc, bins.sisf, home, filepath.Join(root, "up-again.log"), env,
			v.Output.Record.CandidateDigest, reh.greeting())
		rep.phase("restarts (units restarted, then sisf down and sisf up)", started)
	}
}

// upOnlyEnv names the switch of the run that spends nothing.
const upOnlyEnv = "SISF_SMOKE_UP_ONLY"

// upOnlySignIn signs the operator in and proves the sign-in is good for the
// surfaces with a read that starts nothing: the budget. It is the last step
// of the run that spends nothing.
func upOnlySignIn(ctx context.Context, r *commandRunner, sisf, home string) {
	r.t.Helper()
	obs, err := newFactoryObserver(r, sisf, home)
	if err != nil {
		r.fatalf("%v", err)
	}
	obs.signIn(ctx)
	b, err := obs.budget(ctx)
	if err != nil {
		r.fatalf("budget.get as the signed-in operator: %v", err)
	}
	r.report.check("operator signed in", statusPass, "budget.get answered: "+b.summary())
	checkForcedRenewal(ctx, r, obs)
}

// approvePolicy puts the sandbox's current head in force as the policy,
// as the Owner does on the factory's host: the install's own factoryd with
// the control zone's environment.
func approvePolicy(ctx context.Context, r *commandRunner, home string) {
	r.t.Helper()
	revision, err := defaultBranchRevision(ctx, r)
	if err != nil {
		r.fatalf("the sandbox's head commit cannot be read: %v", err)
	}
	l := install.Layout{Dir: home}
	m, err := l.LoadManifest()
	if err != nil {
		r.fatalf("the install's factory.yaml cannot be read: %v", err)
	}
	ext, _ := m.Extensions["github_api_url"].(string)
	env := append(minimalEnv(), "SISF_CONFIG="+l.Config(), "SISF_FACTORY_ID="+string(m.FactoryID), "SISF_GITHUB_API_URL="+ext)
	env = append(env, envLines(r, filepath.Join(l.SecretsDir("control"), "runtime.env"))...)
	cmd := l.Bin("factoryd")
	out, err := r.run(ctx, time.Minute, env, cmd, "policy", "approve", "--revision", revision)
	if err != nil {
		r.fatalf("factoryd policy approve: %v", err)
	}
	r.logf("%s", out)
}

// envLines reads an env file of the install as KEY=VALUE entries.
func envLines(r *commandRunner, path string) []string {
	r.t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		r.fatalf("the env file %s cannot be read", filepath.Base(path))
	}
	var out []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") && strings.Contains(line, "=") {
			out = append(out, line)
		}
	}
	return out
}

// seedInstallSecrets teaches the scrubber every secret the install holds:
// env files as environment, every other secret file by its whole value.
func seedInstallSecrets(r *commandRunner, home string) {
	dir := filepath.Join(home, "secrets")
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // a missing directory means nothing to learn yet
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.HasSuffix(path, ".env") {
			r.scrub.addEnvFile(string(raw))
		} else {
			r.scrub.addSecret(string(raw))
		}
		return nil
	})
}

// installedFactoryID reads the factory ID `sisf init` chose; the runner
// filter admits only that factory's runners.
func installedFactoryID(r *commandRunner, home string) core.FactoryID {
	r.t.Helper()
	m, err := install.Layout{Dir: home}.LoadManifest()
	if err != nil {
		r.fatalf("the install's factory.yaml cannot be read: %v", err)
	}
	return m.FactoryID
}

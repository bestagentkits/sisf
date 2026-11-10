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
func TestRealLifecycleSmoke(t *testing.T) {
	// The temporary directory is created first so that it is removed last,
	// after the teardown and the report have read the install's logs.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, upLog := filepath.Join(root, "factory"), filepath.Join(root, "up.log")
	rep := &smokeReport{Started: time.Now(), Repo: os.Getenv(repoEnv)}
	scrub := newSecretScrubber(os.Environ())
	r := &commandRunner{t: t, dir: repoRoot(), scrub: scrub, report: rep}
	stage := "preflight (no side effect)"
	t.Cleanup(func() {
		rep.Finished = time.Now()
		switch {
		case t.Failed():
			rep.Outcome = "FAIL during " + stage
			rep.Evidence = append(rep.Evidence, logTails(upLog, home)...)
		default:
			rep.Outcome = "PASS"
		}
		path := filepath.Join(repoRoot(), reportRelPath)
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
	facts := preflight(ctx, r)
	choices := choicesFrom(os.Getenv, filepath.Join(root, "sandbox"))
	requireOutsideHome(r, home, choices.operatorHome)
	bins := buildBinaries(ctx, r, filepath.Join(root, "bin"))
	probeFlags(ctx, r, flagProbes(bins, home, repoRoot(), choices))
	// The coding agent's workspaces are checked out from a local clone of
	// the private sandbox, made with the operator's gh login; no credential
	// is written into the install.
	if _, err := r.run(ctx, 5*time.Minute, nil, "gh", "repo", "clone", sandboxRepo, choices.workspaceSource); err != nil {
		r.fatalf("clone the sandbox for the coding agent's workspaces: %v", err)
	}
	rep.phase("preflight and build", started)

	// The build sandbox spends nothing; a sandbox that cannot build the
	// sandbox's head stops the run before the factory starts.
	stage, started = "build sandbox", time.Now()
	sb := newBuildSandbox(r, root)
	sb.provision(ctx, r, "https://github.com/"+sandboxRepo+".git", codexVersion(ctx, r))
	sb.checkIsolation(ctx, r)
	choices.sandbox = sb.choice()
	rep.phase("build sandbox", started)

	stage, started = "install", time.Now()
	env := sisfEnv("SISF_HOME=" + home)
	out, err := r.run(ctx, 2*time.Minute, env, bins.sisf, initArgs(home, choices)...)
	seedInstallSecrets(r, home)
	if err != nil {
		r.fatalf("sisf init: %v", err)
	}
	r.logf("sisf init:\n%s", out)
	factoryID := installedFactoryID(r, home)
	sb.startFilter(ctx, r, bins.sisf, factoryID, filepath.Join(root, "docker-filter.log"))
	head, err := defaultBranchRevision(ctx, r)
	if err != nil {
		r.fatalf("the sandbox's head commit cannot be read: %v", err)
	}
	sb.selfBuild(ctx, r, factoryID, core.CommitSHA(head))
	rep.check("released artifact is the sandboxed build", statusNotChecked,
		"Standalone's local deploy backend serves the installed application rather than the candidate build for every release; "+
			"the sandboxed build is tested and digested, not deployed, so the run looks for the requested change in the merged commit instead")
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
		rep.check("sisf doctor", statusPass, scrub.clean(lastLines(out, 5)))
	}
	approvePolicy(ctx, r, home)
	obs, err := newFactoryObserver(r, bins.sisf, home)
	if err != nil {
		r.fatalf("%v", err)
	}
	signInOperator(ctx, r, bins.sisf, home, obs.env)
	rep.phase("install, up, doctor and policy", started)

	stage, started = "run", time.Now()
	number, url, err := openIssue(ctx, r)
	if err != nil {
		r.fatalf("open the issue: %v", err)
	}
	rep.IssueURL = url
	issue := sandboxRepo + "#" + strconv.Itoa(number)
	r.logf("opened %s", url)
	runErr := waitForClose(ctx, r, obs, proc, issue, number)
	rep.phase("run (issue opened to closed)", started)
	if runErr != nil {
		r.errorf("%v", runErr)
	}

	// The checks run even after a failed run: their evidence goes in the report.
	stage, started = "assertions", time.Now()
	assertOutcome(ctx, r, obs, facts.login, issue, number, rep.Started)
	rep.phase("assertions", started)
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

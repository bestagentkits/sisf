//go:build smoke

package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// standInChoices are the run's machine settings with stand-in paths and a
// stand-in build sandbox.
func standInChoices() runChoices {
	c := choicesFrom(func(k string) string {
		return map[string]string{"HOME": "/Users/operator"}[k]
	}, "/srv/smoke/sandbox")
	c.sandbox = sandboxChoice{endpoint: "http://127.0.0.1:17480", network: "sisf-smoke-x-sandbox", egressProxy: egressProxyURL,
		runnerImage: "127.0.0.1:17481/sisf-smoke-runner@sha256:" + strings.Repeat("a", 64), qaLauncher: "/srv/smoke/bin/qa-codex-launcher"}
	return c
}

// flagValue returns the value that follows flag in args.
func flagValue(args []string, flag string) (string, bool) {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return "", false
	}
	return args[i+1], true
}

func TestTheRunsInitLineUsesTheTypedFlagsOfADevelopmentInstall(t *testing.T) {
	t.Parallel()
	c := standInChoices()
	args := initArgs("/srv/smoke/factory", c)
	for flag, want := range map[string]string{
		"--mode": "standalone", "--dir": "/srv/smoke/factory", "--repo": sandboxRepo, "--github-credential": "gh-cli",
		"--budget-cap": budgetCapUnits, "--alert-threshold": alertThresholdUnits,
		"--coder-agent": "claude", "--coder-model": defaultCoderModel,
		"--qa-judge": "agent", "--qa-agent": "codex", "--qa-model": defaultQAModel,
		"--agent-home": "/Users/operator", "--workspace-source": "/srv/smoke/sandbox",
		"--exec": "docker", "--exec-docker-endpoint": c.sandbox.endpoint, "--exec-docker-network": c.sandbox.network,
		"--egress-proxy": egressProxyURL, "--runner-image": c.sandbox.runnerImage,
		"--qa-agent-binary": c.sandbox.qaLauncher, "--build-command": sandboxBuildCommand,
	} {
		if got, ok := flagValue(args, flag); !ok || got != want {
			t.Errorf("%s = %q (%v), want %q", flag, got, ok, want)
		}
	}
	if !slices.Contains(args, "--dev") {
		t.Error("the run is not a development install")
	}
	line := strings.Join(args, " ")
	// No untyped per-unit flags, no simulation-only release tuning and no
	// token on the command line.
	// --qa-home is refused under --exec docker: the launcher owns the QA
	// agent's home.
	for _, banned := range []string{"--dispatcher-arg", "--supervisor-arg", "--min-hold", "--release-window", "--github-token-env",
		"--profile", "--oidc", "--owner", "--qa-home", "--exec local"} {
		if strings.Contains(line, banned) {
			t.Errorf("the init line carries %s: %s", banned, line)
		}
	}
	if up := strings.Join(upArgs("/srv/smoke/factory", "/src"), " "); up != "up --dir /srv/smoke/factory --mode standalone --source /src" {
		t.Errorf("up line = %q", up)
	}
}

func TestTheRunsModelsAndBuildCommandComeFromTheEnvironment(t *testing.T) {
	t.Parallel()
	env := map[string]string{"HOME": "/h", coderModelEnv: "claude-opus-4-1", qaModelEnv: "o3", buildCommandEnv: "make build"}
	c := choicesFrom(func(k string) string { return env[k] }, "/clone")
	if c.coderModel != "claude-opus-4-1" || c.qaModel != "o3" || c.buildCommand != "make build" || c.operatorHome != "/h" {
		t.Fatalf("choices = %+v", c)
	}
	if !within("/h/x/factory", "/h") || within("/hx/factory", "/h") || !within("/h", "/h") {
		t.Fatal("within misjudges which directory lies below another")
	}
}

// The built sisf parses every command line of the run: with --help after
// them, every flag is checked and nothing runs, so this calls no gh,
// claude or codex.
func TestTheBuiltSisfAcceptsEveryFlagOfTheRun(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "sisf")
	build := exec.Command("go", "build", "-o", bin, "./cmd/sisf")
	build.Dir = repoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build sisf: %v\n%s", err, out)
	}
	dir := filepath.Join(t.TempDir(), "factory")
	for _, p := range flagProbes(binaries{sisf: bin}, dir, repoRoot(), standInChoices()) {
		cmd := exec.Command(p.bin, append(append([]string(nil), p.args...), "--help")...)
		cmd.Env, cmd.WaitDelay = []string{"PATH=/nonexistent", "HOME=" + t.TempDir()}, 5*time.Second
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s refuses the run's flags: %v\n%s", p.what, err, out)
		}
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("a probe with --help created the install")
	}
}

func TestTheInstallsNewSecretsAreRedactedFromTheReport(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	counters := "counters-token-" + strings.Repeat("c", 30)
	client := "oidc-client-secret-" + strings.Repeat("o", 20)
	cli := "sisf_pat_" + strings.Repeat("p", 30)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(home, "secrets", "control"), 0o700))
	must(os.WriteFile(filepath.Join(home, "secrets", "control", "runtime.env"),
		[]byte("SISF_ZONE=control\nEDGE_COUNTERS_TOKEN="+counters+"\nSISF_AUTH_OIDC_CLIENT_SECRET="+client+"\n"), 0o600))
	r := &commandRunner{t: t, scrub: newSecretScrubber(nil), report: &smokeReport{}}
	seedInstallSecrets(r, home)
	r.scrub.addSecret(cli)
	got := r.scrub.clean("counters " + counters + " client " + client + " login " + cli + " zone control")
	for _, secret := range []string{counters, client, cli} {
		if strings.Contains(got, secret) {
			t.Fatalf("a secret of the install reached the report: %q", got)
		}
	}
	if !strings.Contains(got, "zone control") {
		t.Fatalf("ordinary text was redacted: %q", got)
	}
}

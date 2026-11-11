//go:build smoke

package smoke

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The run's choices. Amounts on the sisf command line are whole currency
// units; the ledger counts micros.
const (
	// budgetCapUnits caps the whole run at 5 units.
	budgetCapUnits = "5"
	// alertThresholdUnits raises the Owner alert at 2 units.
	alertThresholdUnits = "2"

	issueTitle = "add GET /version returning the build revision"
	// issueBody carries the sections intake reads (internal/intake/sections.go)
	// so an improvement goes straight to Ready: the scope, the affected
	// feature, which the sandbox's FEATURES.md lists, and a Definition of
	// Done. It names no file that holds the rules, which would make the work
	// a policy change for the Owner to approve.
	issueBody = `## Scope

Add a ` + "`GET " + versionRoute + "`" + ` route to the application that answers with the build
revision the process runs as, as plain text.

## Affected features

- ` + sandboxFeature + `

## Definition of Done

- ` + "`GET " + versionRoute + "`" + ` answers 200 with the build revision as plain text.
- A Go test covers the ` + "`" + versionRoute + "`" + ` handler.
- ` + "`go test ./...`" + ` passes.
`
	// sandboxFeature is the feature of the sandbox's FEATURES.md the issue
	// improves.
	sandboxFeature = "Revision reporting"
	// versionRoute is the route the issue asks for; the merged change must
	// add it.
	versionRoute = "/version"
)

var issueLabels = []string{"sisf:ready", "kind:improvement"}

// The models the two agents run, in place of the tier names of the
// sandbox's model routes, and the command that builds a candidate in the
// run's build sandbox (the runner image's script, sandbox/build-candidate.sh,
// which tests and builds every Go package of the sandbox). Each can be
// replaced from the environment without editing the harness.
const (
	coderModelEnv   = "SISF_SMOKE_CODER_MODEL"
	qaModelEnv      = "SISF_SMOKE_QA_MODEL"
	buildCommandEnv = "SISF_SMOKE_BUILD_COMMAND"

	defaultCoderModel   = "claude-sonnet-4-5"
	defaultQAModel      = "gpt-5-codex"
	defaultBuildCommand = sandboxBuildCommand
)

// runChoices are the run's settings that depend on the machine: the
// operator's home, where the claude login is kept, the local clone the
// coding agent's workspaces are checked out from, and the build sandbox.
type runChoices struct {
	operatorHome    string
	workspaceSource string
	coderModel      string
	qaModel         string
	buildCommand    string
	sandbox         sandboxChoice
}

// sandboxChoice is the run's build sandbox as `sisf init` is told of it.
type sandboxChoice struct {
	endpoint, network, egressProxy, runnerImage, qaLauncher string
}

// choice is what `sisf init` is told of a provisioned sandbox.
func (s *buildSandbox) choice() sandboxChoice {
	return sandboxChoice{endpoint: s.endpoint, network: s.network, egressProxy: egressProxyURL,
		runnerImage: s.runnerImage, qaLauncher: s.launcher}
}

// choicesFrom reads the run's settings from the environment.
func choicesFrom(getenv func(string) string, workspaceSource string) runChoices {
	return runChoices{
		operatorHome:    getenv("HOME"),
		workspaceSource: workspaceSource,
		coderModel:      cmp.Or(getenv(coderModelEnv), defaultCoderModel),
		qaModel:         cmp.Or(getenv(qaModelEnv), defaultQAModel),
		buildCommand:    cmp.Or(getenv(buildCommandEnv), defaultBuildCommand),
	}
}

// initArgs creates the development install: Standalone on loopback,
// signing in at the fake identity provider; the GitHub token taken from
// the local gh login by the installer itself, so none is on a command line;
// a capped budget with an alert; claude coding under trusted_code_only with
// the operator's own home, where its login is; candidates built in the
// run's Docker build sandbox, and codex judging through the launcher that
// runs it in a container, as the supervisor requires under --exec docker;
// the real release defaults, since short canary windows are the
// simulation's only; and a short reconciliation interval, since GitHub
// reaches this loopback install by no webhook. The install directory lies outside the operator's
// home, which the installer requires before it lets an agent use that home.
func initArgs(dir string, c runChoices) []string {
	return []string{
		"init", "--dev", "--mode", "standalone", "--dir", dir, "--repo", sandboxRepo,
		"--github-credential", "gh-cli",
		"--budget-cap", budgetCapUnits, "--alert-threshold", alertThresholdUnits,
		"--coder-agent", "claude", "--coder-model", c.coderModel,
		"--qa-judge", "agent", "--qa-agent", "codex", "--qa-model", c.qaModel,
		"--agent-home", c.operatorHome, "--workspace-source", c.workspaceSource,
		"--exec", "docker", "--exec-docker-endpoint", c.sandbox.endpoint, "--exec-docker-network", c.sandbox.network,
		"--egress-proxy", c.sandbox.egressProxy, "--runner-image", c.sandbox.runnerImage,
		"--qa-agent-binary", c.sandbox.qaLauncher, "--build-command", c.buildCommand,
		"--github-reconcile-interval", reconcileInterval,
	}
}

// reconcileInterval is how often the factory asks GitHub for changed
// issues. The development install takes no webhooks, so this is how it
// learns of the run's issue; control's default of a minute would idle.
const reconcileInterval = "15s"

// probeSandbox stands in for the sandbox in the flag probe, which runs
// before anything is provisioned; --help parses the values and uses none.
var probeSandbox = sandboxChoice{endpoint: "http://127.0.0.1:1", network: "sisf-smoke-probe", egressProxy: egressProxyURL,
	runnerImage: "127.0.0.1:1/sisf-smoke-runner@sha256:" + strings.Repeat("0", 64), qaLauncher: "/nonexistent/qa-codex-launcher"}

// upArgs starts the install in the foreground, building from source.
func upArgs(dir, source string) []string {
	return []string{"up", "--dir", dir, "--mode", "standalone", "--source", source}
}

// downArgs stops the factory and every unit it started; the data stays for
// the evidence until the test's temporary directory goes.
func downArgs(dir string) []string { return []string{"down", "--dir", dir} }

// flagProbe is one command line whose flags must all be known to the built
// program. It is run with --help appended, which parses every flag before
// it and then stops: exit 0 means every flag exists, and nothing else runs.
type flagProbe struct {
	what string
	bin  string
	args []string
}

// flagProbes lists every command line the run depends on.
func flagProbes(bins binaries, dir, source string, c runChoices) []flagProbe {
	if c.sandbox == (sandboxChoice{}) {
		c.sandbox = probeSandbox
	}
	return []flagProbe{
		{"sisf init", bins.sisf, initArgs(dir, c)},
		{"sisf up", bins.sisf, upArgs(dir, source)},
		{"sisf down", bins.sisf, downArgs(dir)},
	}
}

// binaries are the programs built from the checkout for the run.
type binaries struct{ sisf string }

func buildBinaries(ctx context.Context, r *commandRunner, dir string) binaries {
	b := binaries{sisf: filepath.Join(dir, "sisf")}
	r.buildBinary(ctx, b.sisf, "./cmd/sisf")
	return b
}

// probeFlags fails the run, before any side effect, when the built program
// does not know a flag the run needs.
func probeFlags(ctx context.Context, r *commandRunner, probes []flagProbe) {
	r.t.Helper()
	for _, p := range probes {
		args := append(append([]string(nil), p.args...), "--help")
		if _, err := r.run(ctx, time.Minute, minimalEnv(), p.bin, args...); err != nil {
			r.fatalf("preflight: %s does not accept the flags this run needs: %v", p.what, err)
		}
	}
}

// requireOutsideHome refuses an install directory inside the operator's
// home, or a home inside it: the coding agent runs with that home and must not
// reach the factory's secrets. The installer refuses the same; checking
// first keeps the run from failing after its preflight.
func requireOutsideHome(r *commandRunner, dir, home string) {
	r.t.Helper()
	if !filepath.IsAbs(home) {
		r.fatalf("preflight: HOME must be the operator's absolute home, where the claude and codex logins are kept")
	}
	if within(dir, home) || within(home, dir) {
		r.fatalf("preflight: the install directory %s and the operator's home %s must lie apart; set TMPDIR outside the home", dir, home)
	}
	if _, err := os.Stat(home); err != nil {
		r.fatalf("preflight: the operator's home cannot be read: %v", err)
	}
}

// within reports whether path is dir or lies below it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && filepath.IsLocal(rel)
}

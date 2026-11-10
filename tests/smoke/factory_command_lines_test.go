//go:build smoke

package smoke

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
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
	issueBody  = "add GET /version returning the build revision"
)

var issueLabels = []string{"sisf:ready", "kind:improvement"}

// The models the two agents run, in place of the tier names of the
// sandbox's model routes, and the command that builds a candidate of the
// sandbox (examples/hello-app, a Go module). Each can be replaced from the
// environment without editing the harness.
const (
	coderModelEnv   = "SISF_SMOKE_CODER_MODEL"
	qaModelEnv      = "SISF_SMOKE_QA_MODEL"
	buildCommandEnv = "SISF_SMOKE_BUILD_COMMAND"

	defaultCoderModel   = "claude-sonnet-4-5"
	defaultQAModel      = "gpt-5-codex"
	defaultBuildCommand = "go build ./..."
)

// runChoices are the run's settings that depend on the machine: the
// operator's home, where the claude and codex logins are kept, and the
// local clone the coding agent's workspaces are checked out from.
type runChoices struct {
	operatorHome    string
	workspaceSource string
	coderModel      string
	qaModel         string
	buildCommand    string
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
// a capped budget with an alert; claude coding and codex judging under
// trusted_code_only, both with the operator's own home, where their logins
// are; and the real release defaults, since short canary windows are the
// simulation's only. The install directory lies outside the operator's
// home, which the installer requires before it lets an agent use that home.
func initArgs(dir string, c runChoices) []string {
	return []string{
		"init", "--dev", "--mode", "standalone", "--dir", dir, "--repo", sandboxRepo,
		"--github-credential", "gh-cli",
		"--budget-cap", budgetCapUnits, "--alert-threshold", alertThresholdUnits,
		"--coder-agent", "claude", "--coder-model", c.coderModel,
		"--qa-judge", "agent", "--qa-agent", "codex", "--qa-model", c.qaModel,
		"--exec", "local", "--agent-home", c.operatorHome, "--qa-home", c.operatorHome,
		"--workspace-source", c.workspaceSource,
		"--build-command", c.buildCommand,
	}
}

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
// home, or a home inside it: the agents run with that home and must not
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

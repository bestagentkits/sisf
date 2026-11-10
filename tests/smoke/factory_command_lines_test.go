//go:build smoke

package smoke

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/install"
)

// The run's choices. Amounts on the sisf command line are whole currency
// units, as --alert-threshold already is; the ledger counts micros.
const (
	// budgetCapUnits caps the whole run at 5 units (5_000_000 micros).
	budgetCapUnits = "5"
	// alertThresholdUnits raises the Owner alert at 2 units.
	alertThresholdUnits = "2"
	// runEstimateMicros is what the dispatcher reserves per run before a
	// claim, so the gate can refuse a run the cap cannot cover.
	runEstimateMicros = 1_000_000
	// syntheticHold is the low-traffic hold after the release.
	syntheticHold = "2m"

	issueTitle = "add GET /version returning the build revision"
	issueBody  = "add GET /version returning the build revision"
)

var issueLabels = []string{"sisf:ready", "kind:improvement"}

// initArgs creates the Standalone install: SQLite, the dev-only GitHub
// credential taken from the local gh login, a capped budget with an alert
// threshold, and a short synthetic hold. No token is on the command line:
// with --github-credential gh-cli the installer asks `gh auth token` itself.
func initArgs(dir string) []string {
	return []string{
		"init", "--dir", dir, "--mode", "standalone", "--repo", sandboxRepo,
		"--dev", "--github-credential", "gh-cli",
		"--budget-cap", budgetCapUnits, "--alert-threshold", alertThresholdUnits,
		"--min-hold", syntheticHold,
	}
}

// dispatcherArgs are the agent-zone choices of this run: claude codes, and
// every run reserves runEstimateMicros before it is claimed.
func dispatcherArgs() []string {
	return []string{"--agent=claude", "--run-estimate-micros=" + strconv.Itoa(runEstimateMicros)}
}

// supervisorArgs are the supervisor's choices: the coder route it checks
// independence against, and codex as the QA judge, so the route that writes
// never scores its own work.
func supervisorArgs() []string {
	return []string{"--coder-agent=claude", "--qa-judge=agent", "--qa-agent=codex"}
}

// upArgs starts the install in the foreground, building from source. The
// per-unit flags travel through --dispatcher-arg and --supervisor-arg, which
// the installer appends to that unit's command line and nothing else's.
//
// When the installer does not offer those two flags, the capability probe
// stops the run before any side effect, and the report lists the unit
// command lines of manualUnitCommandLines: the operator who wants to run
// without them starts the install with `sisf up` and replaces the two units
// by hand with exactly those lines.
func upArgs(dir, source string) []string {
	args := []string{"up", "--dir", dir, "--mode", "standalone", "--source", source}
	for _, a := range dispatcherArgs() {
		args = append(args, "--dispatcher-arg="+a)
	}
	for _, a := range supervisorArgs() {
		args = append(args, "--supervisor-arg="+a)
	}
	return args
}

// manualUnitCommandLines are the dispatcher and supervisor command lines of
// this run as the Standalone installer starts those units, with the run's
// flags appended. Each runs from the install directory with the env file of
// its own zone only: secrets/agent/runtime.env for the dispatcher and
// secrets/control/supervisor.env for the supervisor.
func manualUnitCommandLines(dir string) []string {
	l := install.Layout{Dir: dir}
	dispatcher := append([]string{l.Bin("factoryd"), "dispatcher"}, dispatcherArgs()...)
	supervisor := append([]string{
		l.Bin("sisf-supervisor"), "run", "--factory-id=" + defaultFactoryID, "--manifest=" + l.ReleaseManifest(),
		"--root-key-ref=env:SISF_ROOT_PUBLIC_KEY", "--trust-dir=" + l.Data("trust"),
		"--admin=127.0.0.1:" + strconv.Itoa(install.PortSupervisor),
	}, supervisorArgs()...)
	return []string{
		"SISF_LISTEN=127.0.0.1:" + strconv.Itoa(install.PortDispatcher) + " " + strings.Join(dispatcher, " "),
		strings.Join(supervisor, " "),
	}
}

// defaultFactoryID is the factory_id `sisf init` derives from the sandbox.
const defaultFactoryID = "sisf-sisf-sandbox"

// downArgs stops the factory and every unit it started; the data stays for
// the evidence until the test's temporary directory goes.
func downArgs(dir string) []string { return []string{"down", "--dir", dir} }

// flagProbe is one command line whose flags must all be known to the built
// programs. It is run with --help appended, which parses every flag before
// it and then stops: exit 0 means every flag exists, and nothing else runs.
type flagProbe struct {
	what string
	bin  string
	args []string
}

// flagProbes lists every command line the run depends on.
func flagProbes(bins binaries, dir, source string) []flagProbe {
	return []flagProbe{
		{"sisf init", bins.sisf, initArgs(dir)},
		{"sisf up", bins.sisf, upArgs(dir, source)},
		{"factoryd dispatcher", bins.factoryd, append([]string{"dispatcher"}, dispatcherArgs()...)},
		{"supervisor run", bins.supervisor, append([]string{"run"}, supervisorArgs()...)},
	}
}

// binaries are the programs built from the checkout for the run.
type binaries struct{ sisf, factoryd, supervisor string }

func buildBinaries(ctx context.Context, r *commandRunner, dir string) binaries {
	b := binaries{
		sisf:       filepath.Join(dir, "sisf"),
		factoryd:   filepath.Join(dir, "factoryd"),
		supervisor: filepath.Join(dir, "sisf-supervisor"),
	}
	r.buildBinary(ctx, b.sisf, "./cmd/sisf")
	r.buildBinary(ctx, b.factoryd, "./cmd/factoryd")
	r.buildBinary(ctx, b.supervisor, "./cmd/supervisor")
	return b
}

// probeFlags fails the run, before any side effect, when a built program
// does not know a flag the run needs.
func probeFlags(ctx context.Context, r *commandRunner, probes []flagProbe, dir string) {
	r.t.Helper()
	for _, p := range probes {
		args := append(append([]string(nil), p.args...), "--help")
		if _, err := r.run(ctx, time.Minute, minimalEnv(), p.bin, args...); err != nil {
			if p.what == "sisf up" {
				r.report.Deviations = append(r.report.Deviations, prefixed(
					"manual unit command line (replace the installer's unit with it): `", manualUnitCommandLines(dir), "`")...)
			}
			r.fatalf("preflight: %s does not accept the flags this run needs; the install must add them first: %v", p.what, err)
		}
	}
}

package install

import (
	"os"
	"strconv"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// appLauncher is the program the supervisor's local deployment backend
// starts for each release of the application, with the release's image
// reference as its first argument and, for a release whose program a
// candidate build exported, that program's file in the supervisor's
// program store as its second.
const appLauncher = "sisf-app-launcher"

// syntheticProgram is the application's synthetic suite, built from the
// application's source. The supervisor runs it against a release whose
// real traffic is too thin to judge it.
const syntheticProgram = "hello-app-synthetic"

// standaloneSupervisorArgs is the supervisor's command line in Standalone
// mode: native processes, the local deployment backend, the local
// execution backend or, when the install has one, its Docker build sandbox,
// and the SQLite store its env file names.
func (in *Installer) standaloneSupervisorArgs(m *manifest.Manifest, c LifecycleChoice) []string {
	l := in.Layout
	args := []string{
		"run", "--factory-id=" + string(m.FactoryID), "--manifest=" + l.ReleaseManifest(),
		"--root-key-ref=" + rootKeyRef, "--trust-dir=" + l.Data("trust"), "--admin=" + addr(PortSupervisor),
		"--dispatcher-admin-url=http://" + addr(PortDispatcherAdmin),
	}
	if !c.Enabled {
		return args
	}
	args = append(args, c.BuildSandbox.supervisorArgs()...)
	args = append(args,
		"--factory-manifest="+l.Config(), "--coder-agent="+c.CoderAgent, "--lifecycle-interval="+c.Interval,
		"--deploy=local", "--edge-admin-url=http://"+addr(PortEdgeAdmin),
		"--deploy-local-program="+l.Bin(appLauncher), "--qa-judge="+c.QAJudge,
	)
	if m.Release.LowTraffic.SyntheticSuite != "" {
		args = append(args, "--synthetic-program="+l.Bin(syntheticProgram))
	}
	if c.QAJudge == "agent" {
		args = append(args, "--qa-agent="+c.QAAgent)
		if c.QAAgentBinary != "" {
			args = append(args, "--qa-agent-binary="+c.QAAgentBinary)
		}
		if c.Agents.QAModel != "" {
			args = append(args, "--qa-model="+c.Agents.QAModel)
		}
		if c.Agents.QAHome != "" && !c.BuildSandbox.set() {
			args = append(args, "--qa-home="+c.Agents.QAHome)
		}
	}
	if c.BuildCommand != "" {
		args = append(args, "--build-command="+c.BuildCommand)
	}
	if c.ReleaseWindow != "" {
		args = append(args, "--release-window="+c.ReleaseWindow, "--release-min-samples="+strconv.Itoa(c.ReleaseMinSamples))
	}
	return args
}

// writeAppLauncher writes the launcher the supervisor starts a release of
// the application with. The release's reference is not used to pick a
// program: the supervisor does that, and gives the launcher the file of
// the program a candidate build exported, or nothing for the installed
// release, which is then the install's own application. The revision the
// program reports, SISF_REVISION, and the address it listens on,
// SISF_LISTEN, come from the backend.
func (in *Installer) writeAppLauncher() error {
	l := in.Layout
	for _, p := range []string{l.Bin("hello-app"), l.Data("app")} {
		// The script single-quotes both paths, so single spaces are safe.
		if !quotedPathPattern.MatchString(p) {
			return invalidf("the install path %q has characters a launcher script cannot carry; use letters, digits, single spaces and . _ + - /", p)
		}
	}
	script := "#!/bin/sh\n" +
		"# Started by the supervisor for each release of the application; written\n" +
		"# by `sisf up`, which replaces it. $1 is the release's image reference;\n" +
		"# $2, when given, is the file of the program a candidate build exported,\n" +
		"# which the supervisor checked against the release's digest. Without it\n" +
		"# the installed application is started.\n" +
		"set -eu\n" +
		"program='" + l.Bin("hello-app") + "'\n" +
		"if [ \"$#\" -ge 2 ]; then\n" +
		"\tprogram=$2\n" +
		"fi\n" +
		"HELLO_ADDR=\"$SISF_LISTEN\" HELLO_DATA_DIR='" + l.Data("app") + "' exec \"$program\"\n"
	path := l.Bin(appLauncher)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return unavailablef(err, "cannot write the application launcher")
	}
	// WriteFile keeps the mode of a file that already exists.
	if err := os.Chmod(path, 0o755); err != nil {
		return unavailablef(err, "cannot make the application launcher executable")
	}
	return nil
}

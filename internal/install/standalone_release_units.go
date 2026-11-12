package install

import (
	"os"
	"strconv"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// appLauncher is the program the supervisor's local deployment backend
// starts for each release of the application, with the release's image
// reference as its only argument.
const appLauncher = "sisf-app-launcher"

// standaloneSupervisorArgs is the supervisor's command line in Standalone
// mode: native processes, the local execution and deployment backends, and
// the SQLite store its env file names.
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
	args = append(args,
		"--factory-manifest="+l.Config(), "--coder-agent="+c.CoderAgent, "--lifecycle-interval="+c.Interval,
		"--exec=local", "--deploy=local", "--edge-admin-url=http://"+addr(PortEdgeAdmin),
		"--deploy-local-program="+l.Bin(appLauncher), "--qa-judge="+c.QAJudge,
	)
	if c.QAJudge == "agent" {
		args = append(args, "--qa-agent="+c.QAAgent)
		if c.QAAgentBinary != "" {
			args = append(args, "--qa-agent-binary="+c.QAAgentBinary)
		}
	}
	if c.ReleaseWindow != "" {
		args = append(args, "--release-window="+c.ReleaseWindow, "--release-min-samples="+strconv.Itoa(c.ReleaseMinSamples))
	}
	return args
}

// writeAppLauncher writes the launcher the supervisor starts a release of
// the application with. The release's reference is not used to pick a
// program: the install has one application program, and the revision it
// reports, SISF_REVISION, and the address it listens on, SISF_LISTEN, come
// from the backend.
func (in *Installer) writeAppLauncher() error {
	l := in.Layout
	for _, p := range []string{l.Bin("hello-app"), l.Data("app")} {
		if !pathPattern.MatchString(p) {
			return invalidf("the install path %q has characters a launcher script cannot carry; use letters, digits and . _ + - /", p)
		}
	}
	script := "#!/bin/sh\n" +
		"# Started by the supervisor for each release of the application; written\n" +
		"# by `sisf up`, which replaces it.\n" +
		"set -eu\n" +
		"HELLO_ADDR=\"$SISF_LISTEN\" HELLO_DATA_DIR='" + l.Data("app") + "' exec '" + l.Bin("hello-app") + "'\n"
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

package install

import "github.com/bestagentkits/sisf/pkg/manifest"

// noBuildSandbox says why a Standalone install outside the simulation
// cannot run without a Docker build sandbox. Its supervisor builds
// candidates only in a sandbox and refuses the local execution backend,
// which cannot isolate untrusted code (cmd/supervisor/stages/execution_policy.go);
// in Standalone mode the supervisor is also what runs the application. So
// such an install is refused once by `sisf up` rather than started with a
// supervisor that refuses, and is restarted, forever.
const noBuildSandbox = "a Standalone install outside the simulation needs a Docker build sandbox: its supervisor builds candidates only in one, " +
	"refuses the local execution backend, which cannot isolate untrusted code, and is what runs the application, so `sisf up` starts nothing without it; " +
	"create the install with --exec docker --exec-docker-endpoint URL --exec-docker-network NET --egress-proxy URL --runner-image IMAGE " +
	"--build-command CMD --qa-agent-binary LAUNCHER (docs/guides/install.md#real-agents-in-standalone), or use --profile sim to try the factory"

// needsBuildSandbox reports whether options of mode lack the build sandbox
// their supervisor requires.
func needsBuildSandbox(o Options, mode manifest.Mode) bool {
	return mode == manifest.ModeStandalone && !o.Sim && !o.BuildSandbox.set()
}

// standaloneRunnable refuses a Standalone install that cannot start.
func (in *Installer) standaloneRunnable(m *manifest.Manifest) error {
	o, err := in.installedOptions(m)
	if err != nil {
		return err
	}
	if needsBuildSandbox(o, m.Mode) {
		return invalidf("%s", noBuildSandbox)
	}
	return nil
}

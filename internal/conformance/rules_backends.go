package conformance

import (
	"fmt"
	"slices"

	kit "github.com/bestagentkits/sisf/pkg/conformance"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkBackends verifies that the operating mode has a supported profile and
// that the execution and deployment backends the manifest selects are in the
// capability matrix, run in that mode, and do not lack what the manifest
// asks of them. A capability a backend only claims passes here; whether it
// is proven is decided when the release is attempted. A capability a backend
// declares absent, or never declared, does not.
func (c *checker) checkBackends() {
	if !c.m.Mode.Valid() {
		c.add(RuleModeUnsupported, "mode",
			fmt.Sprintf("set mode to %s or %s", manifest.ModeStandalone, manifest.ModeDocker),
			"mode %q has no supported deployment profile; every other platform stays unsupported until it is proven end to end", string(c.m.Mode))
	}
	c.selected(kit.KindExecution, "adapters.execution.name", c.m.Adapters.Execution.Name)
	deployer, ok := c.selected(kit.KindDeploy, "adapters.deploy.name", c.m.Adapters.Deploy.Name)
	if !ok {
		return
	}

	// The release strategy needs a matching way to switch versions.
	switching := map[manifest.Strategy]core.Capability{
		manifest.StrategyBlueGreen: core.CapDeployBlueGreen,
		manifest.StrategyCanary:    core.CapDeployCanary,
	}
	if needed, known := switching[c.m.Release.Strategy]; known && lacks(deployer, needed) {
		c.add(RuleBackendCapability, "release.strategy",
			"choose a release strategy the deployment backend supports, or another deployment backend",
			"release.strategy is %s, but deployment backend %q declares no %s", c.m.Release.Strategy, deployer.Name, needed)
	}
	// Publishing without a person needs a way back and a way to observe.
	if len(c.m.Release.Units) > 0 {
		for _, needed := range []core.Capability{core.CapDeployRollback, core.CapDeployObservedRevision, core.CapDeployMetrics} {
			if lacks(deployer, needed) {
				c.add(RuleBackendCapability, "release.units",
					"empty release.units, or choose a deployment backend that can roll back and be observed",
					"release.units lets the factory publish by itself, but deployment backend %q declares no %s; a release that cannot be observed and undone is held, not published", deployer.Name, needed)
			}
		}
	}
}

// selected looks up the backend a manifest names and checks that it is
// declared and runs in the manifest's mode. ok is false when it is not in
// the capability matrix.
func (c *checker) selected(kind kit.Kind, path, name string) (kit.Declaration, bool) {
	declaration, declared := kit.Find(c.backends, kind, name)
	if !declared {
		c.add(RuleBackendUndeclared, path,
			fmt.Sprintf("select one of the declared %s backends: %v", kind, declaredNames(c.backends, kind)),
			"%s backend %q is not in the capability matrix; a backend declares its capabilities before it is used", kind, name)
		return kit.Declaration{}, false
	}
	if c.m.Mode.Valid() && !slices.Contains(declaration.Modes, c.m.Mode) {
		c.add(RuleBackendMode, path,
			fmt.Sprintf("select a %s backend that runs in %s mode, or change mode", kind, c.m.Mode),
			"%s backend %q does not run in %s mode; it runs in %v", kind, name, c.m.Mode, declaration.Modes)
	}
	return declaration, true
}

// lacks reports whether the backend declares the capability absent or does
// not declare it at all.
func lacks(d kit.Declaration, capability core.Capability) bool {
	return d.Capabilities.Level(capability) == core.SupportNotSupported
}

// declaredNames lists the names of the declared backends of one kind.
func declaredNames(declarations []kit.Declaration, kind kit.Kind) []string {
	var names []string
	for _, d := range declarations {
		if d.Kind == kind {
			names = append(names, d.Name)
		}
	}
	slices.Sort(names)
	return names
}

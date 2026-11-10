package conformance

import (
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Builtin returns the declarations of the execution and deployment backends
// of the two supported operating modes. A manifest may select only a backend
// declared here, because capabilities are declared before use.
//
// The levels are design-time claims: every one is unverified, so no built-in
// backend is graded above experimental until a run proves it. What is absent
// is stated as plainly as what is claimed.
func Builtin() []Declaration {
	const claimed, absent = core.SupportUnverified, core.SupportNotSupported
	standalone, docker := manifest.ModeStandalone, manifest.ModeDocker
	return []Declaration{
		{
			// Local processes share the machine with the factory: suitable
			// for code the Owner trusts and for nothing else.
			Kind: KindExecution, Name: "local", Modes: []manifest.Mode{standalone},
			Capabilities: core.CapabilitySet{
				core.CapLifecycle: claimed, core.CapRollback: claimed, core.CapObservability: claimed,
				core.CapExecIsolation: absent, core.CapExecEgressEnforcement: absent,
				core.CapExecResourceLimits: absent, core.CapExecSnapshot: absent,
			},
		},
		{
			Kind: KindExecution, Name: "docker", Modes: []manifest.Mode{standalone, docker},
			Capabilities: core.CapabilitySet{
				core.CapLifecycle: claimed, core.CapRollback: claimed, core.CapObservability: claimed,
				core.CapExecIsolation: claimed, core.CapExecEgressEnforcement: claimed,
				core.CapExecResourceLimits: claimed, core.CapExecSnapshot: absent,
			},
		},
		{
			// Two processes behind the edge: one switch, no traffic split.
			Kind: KindDeploy, Name: "local", Modes: []manifest.Mode{standalone},
			Capabilities: core.CapabilitySet{
				core.CapLifecycle: claimed, core.CapIsolation: claimed,
				core.CapDeployBlueGreen: claimed, core.CapDeployCanary: absent, core.CapDeployPreview: claimed,
				core.CapDeployRollback: claimed, core.CapDeployObservedRevision: claimed, core.CapDeployMetrics: claimed,
			},
		},
		{
			Kind: KindDeploy, Name: "compose", Modes: []manifest.Mode{docker},
			Capabilities: core.CapabilitySet{
				core.CapLifecycle: claimed, core.CapIsolation: claimed,
				core.CapDeployBlueGreen: claimed, core.CapDeployCanary: claimed, core.CapDeployPreview: claimed,
				core.CapDeployRollback: claimed, core.CapDeployObservedRevision: claimed, core.CapDeployMetrics: claimed,
			},
		},
	}
}

// Find returns the declaration of the named backend among declarations.
func Find(declarations []Declaration, kind Kind, name string) (Declaration, bool) {
	for _, d := range declarations {
		if d.Kind == kind && d.Name == name {
			return d, true
		}
	}
	return Declaration{}, false
}

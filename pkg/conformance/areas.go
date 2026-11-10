package conformance

import "github.com/bestagentkits/sisf/internal/core"

// areaEvidence says which declared capabilities each area rests on, per
// interface. An area is as strong as its weakest group, and a group is as
// strong as its strongest member, so a group lists alternatives.
//
// Where an interface has a capability of its own for an area, the area is
// graded from it; otherwise the backend declares the area by its own name.
// What each area means for each interface:
//
//	             lifecycle             isolation                rollback                 observability
//	agent        start, status, end    stays in its workspace   a run can be cancelled   status and events
//	execution    create, run, destroy  proven isolation and     destroy leaves nothing   a sandbox can be
//	                                   enforced egress policy   behind                   inspected
//	deploy       stage and switch by   a staged candidate does  return to the last       observed revision
//	             blue-green or canary  not touch live traffic   known good release       and metrics
var areaEvidence = map[Kind]map[core.Capability][][]core.Capability{
	KindAgent: {
		core.CapRollback: {{core.CapAgentCancel}},
	},
	KindExecution: {
		core.CapIsolation: {{core.CapExecIsolation}, {core.CapExecEgressEnforcement}},
	},
	KindDeploy: {
		core.CapLifecycle:     {{core.CapLifecycle}, {core.CapDeployBlueGreen, core.CapDeployCanary}},
		core.CapRollback:      {{core.CapDeployRollback}},
		core.CapObservability: {{core.CapDeployObservedRevision}, {core.CapDeployMetrics}},
	},
}

// areaLevel grades one area of a backend from its declared capabilities.
func areaLevel(kind Kind, area core.Capability, declared core.CapabilitySet) core.SupportLevel {
	groups, ok := areaEvidence[kind][area]
	if !ok {
		groups = [][]core.Capability{{area}}
	}
	level := core.SupportSupported
	for _, group := range groups {
		best := core.SupportNotSupported
		for _, capability := range group {
			best = stronger(best, declared.Level(capability))
		}
		level = weaker(level, best)
	}
	return level
}

// strength orders the support levels: verified, then claimed, then absent.
func strength(level core.SupportLevel) int {
	switch level {
	case core.SupportSupported:
		return 2
	case core.SupportUnverified:
		return 1
	}
	return 0
}

// weaker returns the less reliable of two levels.
func weaker(a, b core.SupportLevel) core.SupportLevel {
	if strength(b) < strength(a) {
		return b
	}
	return a
}

// stronger returns the more reliable of two levels.
func stronger(a, b core.SupportLevel) core.SupportLevel {
	if strength(b) > strength(a) {
		return b
	}
	return a
}

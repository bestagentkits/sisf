package core

// Capability names something a backend or deployment profile may be able to
// do. Capabilities are declared before use, so the factory never discovers a
// missing one halfway through a run.
type Capability string

// Profile capabilities: a deployment profile lacking any of them is
// experimental or unsupported and must not be described as a full deployment.
const (
	CapSingleFactoryDeployment Capability = "single_factory_deployment"
	CapDurableTasks            Capability = "durable_tasks"
	CapSafeExec                Capability = "safe_exec"
	CapReleasableZone1         Capability = "releasable_zone1"
	CapRollback                Capability = "rollback"
)

// Conformance areas graded for every backend, together with CapRollback.
const (
	CapLifecycle     Capability = "lifecycle"
	CapIsolation     Capability = "isolation"
	CapObservability Capability = "observability"
)

// Agent backend capabilities.
const (
	CapAgentCancel      Capability = "agent.cancel"
	CapAgentReplay      Capability = "agent.replay"
	CapAgentResume      Capability = "agent.resume"
	CapAgentCheckpoint  Capability = "agent.checkpoint"
	CapAgentUsage       Capability = "agent.usage"
	CapAgentHumanInput  Capability = "agent.human_input"
	CapAgentSessionLink Capability = "agent.session_link"
	CapAgentWebhooks    Capability = "agent.webhooks"
	// CapAgentHardBudget may be declared only when the provider itself
	// enforces a spending limit.
	CapAgentHardBudget Capability = "agent.hard_budget"
)

// Execution backend capabilities.
const (
	// CapExecIsolation is supported only when isolation of untrusted code is
	// proven; being hosted by a provider proves nothing.
	CapExecIsolation Capability = "execution.isolation"
	// CapExecEgressEnforcement is the ability to enforce the egress policy.
	CapExecEgressEnforcement Capability = "execution.egress_enforcement"
	CapExecResourceLimits    Capability = "execution.resource_limits"
	CapExecSnapshot          Capability = "execution.snapshot"
)

// Deployment backend capabilities.
const (
	CapDeployPreview   Capability = "deploy.preview"
	CapDeployBlueGreen Capability = "deploy.blue_green"
	// CapDeployCanary is weighted traffic splitting between two revisions.
	CapDeployCanary           Capability = "deploy.canary"
	CapDeployMetrics          Capability = "deploy.metrics"
	CapDeployObservedRevision Capability = "deploy.observed_revision"
	CapDeployRollback         Capability = "deploy.rollback"
)

// ProfileCapabilities returns the five capabilities a profile needs before it
// may be called a full deployment.
func ProfileCapabilities() []Capability {
	return []Capability{CapSingleFactoryDeployment, CapDurableTasks, CapSafeExec, CapReleasableZone1, CapRollback}
}

// ConformanceAreas returns the four areas every backend is graded on.
func ConformanceAreas() []Capability {
	return []Capability{CapLifecycle, CapIsolation, CapRollback, CapObservability}
}

// SupportLevel is how far a declared capability can be relied on.
type SupportLevel string

const (
	// SupportSupported means the capability exists and was verified.
	SupportSupported SupportLevel = "supported"
	// SupportUnverified means the capability is claimed but not proven; it
	// is reported as such and never relied on for a safety decision.
	SupportUnverified SupportLevel = "unverified"
	// SupportNotSupported means the capability is absent.
	SupportNotSupported SupportLevel = "NOT_SUPPORTED"
)

// Valid reports whether l is a known support level.
func (l SupportLevel) Valid() bool {
	return l == SupportSupported || l == SupportUnverified || l == SupportNotSupported
}

// CapabilitySet is what a backend declares about itself.
type CapabilitySet map[Capability]SupportLevel

// Level returns the declared level, and NOT_SUPPORTED for a capability that
// was never declared or carries an unknown level: silence is not support.
func (s CapabilitySet) Level(c Capability) SupportLevel {
	if level, ok := s[c]; ok && level.Valid() {
		return level
	}
	return SupportNotSupported
}

// Supports reports whether c is verified as supported. Unverified is not enough.
func (s CapabilitySet) Supports(c Capability) bool { return s.Level(c) == SupportSupported }

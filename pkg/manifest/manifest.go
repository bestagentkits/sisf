// Package manifest defines the Go types of factory.yaml, the manifest of one
// factory deployment.
//
// One manifest declares one deployment: a single factory_id, the three zone
// components, the nine runtime units, the trusted release supervisor, storage
// and networking, sharing one release manifest and one lifecycle. The file
// holds desired state approved by the Owner; measured costs and runtime state
// are never kept in it. Every field has the same key in YAML and in JSON.
// Loading, schema and conformance checks live beside this file.
package manifest

import (
	"slices"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// APIVersion is the manifest version these types describe.
const APIVersion = "sisf/v1"

// Mode is how the units of a deployment are realized.
type Mode string

const (
	// ModeStandalone runs the units as supervised processes on one machine.
	ModeStandalone Mode = "standalone"
	// ModeDocker runs one service per unit in a single compose project.
	ModeDocker Mode = "docker"
)

// Valid reports whether m is a mode these types support.
func (m Mode) Valid() bool { return m == ModeStandalone || m == ModeDocker }

// UnitName names a runtime unit. Names are the same on every platform; only
// the realization (process, container, pod, function) changes.
type UnitName string

const (
	UnitApp     UnitName = "app"
	UnitControl UnitName = "control"
	// UnitSupervisor is the only unit that holds deploy rights and the
	// delegated signing keys. It is a separate process from control.
	UnitSupervisor UnitName = "supervisor"
	UnitDispatcher UnitName = "dispatcher"
	// UnitRunner runs the coding agent: one per run, no inbound connections.
	UnitRunner UnitName = "runner"
	// UnitQARunner runs independent QA with an identity and credentials
	// different from the runner's.
	UnitQARunner  UnitName = "qa-runner"
	UnitState     UnitName = "state"
	UnitArtifacts UnitName = "artifacts"
	UnitEdge      UnitName = "edge"
)

var allUnits = []UnitName{UnitApp, UnitControl, UnitSupervisor, UnitDispatcher, UnitRunner, UnitQARunner,
	UnitState, UnitArtifacts, UnitEdge}

// UnitNames returns the nine units. The caller owns the returned slice.
func UnitNames() []UnitName { return slices.Clone(allUnits) }

// Valid reports whether u is one of the nine units.
func (u UnitName) Valid() bool { return slices.Contains(allUnits, u) }

// Zone returns the zone the unit belongs to, core.ZoneShared for the units
// every zone uses, or "" for an unknown unit.
func (u UnitName) Zone() core.Zone {
	switch u {
	case UnitApp:
		return core.ZoneApplication
	case UnitDispatcher, UnitRunner, UnitQARunner:
		return core.ZoneAgent
	case UnitControl, UnitSupervisor:
		return core.ZoneControl
	case UnitState, UnitArtifacts, UnitEdge:
		return core.ZoneShared
	}
	return ""
}

// sharedUnits returns the units every zone uses, in unit order.
func sharedUnits() []UnitName {
	var shared []UnitName
	for _, u := range allUnits {
		if u.Zone().Shared() {
			shared = append(shared, u)
		}
	}
	return shared
}

// Lifecycle returns the lifecycle the unit always has, or "" when unknown.
func (u UnitName) Lifecycle() Lifecycle {
	switch u {
	case UnitRunner, UnitQARunner:
		return LifecycleEphemeral
	case UnitState, UnitArtifacts:
		return LifecycleDurable
	}
	if u.Valid() {
		return LifecycleLongRunning
	}
	return ""
}

// Lifecycle is how long a unit lives.
type Lifecycle string

const (
	LifecycleLongRunning Lifecycle = "long_running"
	// LifecycleEphemeral units exist for one run and are then destroyed.
	LifecycleEphemeral Lifecycle = "ephemeral"
	// LifecycleDurable units hold data that outlives every release.
	LifecycleDurable Lifecycle = "durable"
)

// Valid reports whether l is a known lifecycle.
func (l Lifecycle) Valid() bool {
	return l == LifecycleLongRunning || l == LifecycleEphemeral || l == LifecycleDurable
}

// NetworkName names one of the four network segments.
type NetworkName string

const (
	NetworkApp     NetworkName = "app"
	NetworkFactory NetworkName = "factory"
	NetworkControl NetworkName = "control"
	// NetworkSandbox holds the runners, which reach the outside only through
	// the egress proxy.
	NetworkSandbox NetworkName = "sandbox"
)

var allNetworks = []NetworkName{NetworkApp, NetworkFactory, NetworkControl, NetworkSandbox}

// NetworkNames returns the four segments. The caller owns the returned slice.
func NetworkNames() []NetworkName { return slices.Clone(allNetworks) }

// Valid reports whether n is one of the four segments.
func (n NetworkName) Valid() bool { return slices.Contains(allNetworks, n) }

// Duration is a time span written as text such as "10m" or "72h".
type Duration time.Duration

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// MarshalText renders the duration in Go duration syntax.
func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

// UnmarshalText parses Go duration syntax and rejects negative spans.
func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil || parsed < 0 {
		return core.NewError(core.CodeInvalidArgument, "duration must be a non-negative span such as \"10m\"")
	}
	*d = Duration(parsed)
	return nil
}

// Manifest is the content of factory.yaml.
type Manifest struct {
	APIVersion string `json:"api_version" yaml:"api_version"`
	// FactoryID identifies the deployment for its whole life; DeploymentID
	// names this installation of it. Neither is a runtime or run identifier.
	FactoryID    core.FactoryID    `json:"factory_id" yaml:"factory_id"`
	DeploymentID core.DeploymentID `json:"deployment_id" yaml:"deployment_id"`
	Mode         Mode              `json:"mode" yaml:"mode"`
	Repository   Repository        `json:"repository" yaml:"repository"`
	Components   Components        `json:"components" yaml:"components"`
	// Units declares the runtime units by name. All nine must be present.
	Units map[UnitName]Unit `json:"units" yaml:"units"`
	// Supervisor is nil when undeclared, which disables self-update.
	Supervisor *Supervisor             `json:"supervisor,omitempty" yaml:"supervisor,omitempty"`
	Networks   map[NetworkName]Network `json:"networks" yaml:"networks"`
	Volumes    map[string]Volume       `json:"volumes,omitempty" yaml:"volumes,omitempty"`
	Storage    Storage                 `json:"storage" yaml:"storage"`
	Adapters   Adapters                `json:"adapters" yaml:"adapters"`
	Release    Release                 `json:"release" yaml:"release"`
	Budget     Budget                  `json:"budget" yaml:"budget"`
	Limits     Limits                  `json:"limits" yaml:"limits"`
	// Egress is the outbound policy of agent sandboxes.
	Egress core.EgressPolicy `json:"egress" yaml:"egress"`
	// TrustedCodeOnly is the Owner's acknowledgement that the execution
	// backend can neither isolate agent runs nor enforce the egress policy,
	// so this deployment runs only code the Owner trusts. It is false unless
	// stated; without it such a backend is refused.
	TrustedCodeOnly bool        `json:"trusted_code_only,omitempty" yaml:"trusted_code_only,omitempty"`
	ModelRoutes     ModelRoutes `json:"model_routes" yaml:"model_routes"`
	// Decision is nil when undeclared: the deterministic rules decide alone.
	Decision *Decision `json:"decision,omitempty" yaml:"decision,omitempty"`
	// Extensions carries settings these types do not model yet. Nothing in
	// it can grant a permission or relax a check.
	Extensions map[string]any `json:"extensions,omitempty" yaml:"extensions,omitempty"`
}

// Repository is the GitHub repository that is the source of truth.
type Repository struct {
	Owner string `json:"owner" yaml:"owner"`
	Repo  string `json:"repo" yaml:"repo"`
}

// Components holds the three zone components. A nil component is a missing
// zone; a deployment missing one is not a factory deployment.
type Components struct {
	Application *Component `json:"application,omitempty" yaml:"application,omitempty"`
	Agent       *Component `json:"agent,omitempty" yaml:"agent,omitempty"`
	Control     *Component `json:"control,omitempty" yaml:"control,omitempty"`
}

// Zone returns the component of zone z, or nil when it is not declared or z
// is not one of the three zones.
func (c Components) Zone(z core.Zone) *Component {
	switch z {
	case core.ZoneApplication:
		return c.Application
	case core.ZoneAgent:
		return c.Agent
	case core.ZoneControl:
		return c.Control
	}
	return nil
}

// Missing returns the zones that have no component, in zone order.
func (c Components) Missing() []core.Zone {
	var missing []core.Zone
	for _, z := range core.Zones() {
		if c.Zone(z) == nil {
			missing = append(missing, z)
		}
	}
	return missing
}

// Component is one zone of the deployment. Zones are isolated from each
// other: no two share credentials or a store role.
type Component struct {
	// Source is the directory of the component's code in the repository.
	Source string `json:"source" yaml:"source"`
	// CredentialsRef references the credential set only this zone may use.
	// It is a secret reference, never the credentials themselves.
	CredentialsRef string `json:"credentials_ref" yaml:"credentials_ref"`
	// StoreRole is the role this zone opens the state store with.
	StoreRole string `json:"store_role" yaml:"store_role"`
	// HealthCheck is the zone's own health probe, a path or a command.
	HealthCheck string `json:"health_check,omitempty" yaml:"health_check,omitempty"`
}

// Unit is one runtime unit. Its zone and lifecycle follow from its name.
type Unit struct {
	Image string `json:"image,omitempty" yaml:"image,omitempty"`
	// Digest pins the unit's artifact in the release manifest.
	Digest    core.Digest   `json:"digest,omitempty" yaml:"digest,omitempty"`
	Command   []string      `json:"command,omitempty" yaml:"command,omitempty"`
	Lifecycle Lifecycle     `json:"lifecycle" yaml:"lifecycle"`
	Networks  []NetworkName `json:"networks,omitempty" yaml:"networks,omitempty"`
	Port      int           `json:"port,omitempty" yaml:"port,omitempty"`
	// Public exposes the unit outside the deployment. With PublicRoutes set,
	// only those routes are exposed.
	Public       bool     `json:"public,omitempty" yaml:"public,omitempty"`
	PublicRoutes []string `json:"public_routes,omitempty" yaml:"public_routes,omitempty"`
	Mounts       []Mount  `json:"mounts,omitempty" yaml:"mounts,omitempty"`
	// CredentialsRef references a credential set of the unit's own, used in
	// place of its zone's. The qa-runner names one, so that independent QA
	// never holds the credentials of the runner whose work it judges. It is
	// a secret reference, never the credentials themselves.
	CredentialsRef string `json:"credentials_ref,omitempty" yaml:"credentials_ref,omitempty"`
}

// Mount attaches a declared volume to a unit.
type Mount struct {
	Volume   string `json:"volume" yaml:"volume"`
	Path     string `json:"path" yaml:"path"`
	ReadOnly bool   `json:"read_only,omitempty" yaml:"read_only,omitempty"`
}

// Volume is a named place for files. Zone is the only zone allowed to write
// to it; core.ZoneShared marks the data of the state or artifacts unit, which
// every zone uses through that unit and never through a mount of its own.
type Volume struct {
	Zone core.Zone `json:"zone" yaml:"zone"`
	// Holder names the unit that keeps a shared volume: state, artifacts or
	// edge (its TLS state). Left out, the artifacts unit holds the volume
	// storage.artifacts names and the state unit holds any other. A volume
	// owned by a zone names no holder.
	Holder UnitName `json:"holder,omitempty" yaml:"holder,omitempty"`
}

// Network is one network segment.
type Network struct {
	// Internal segments have no route to the outside.
	Internal bool `json:"internal,omitempty" yaml:"internal,omitempty"`
	// EgressVia names the proxy through which the segment reaches outside.
	EgressVia string `json:"egress_via,omitempty" yaml:"egress_via,omitempty"`
}

// DigestChange says who may change the supervisor's pinned digest.
type DigestChange string

const (
	// DigestChangeGovernance requires a governance cycle approved by a person.
	DigestChangeGovernance DigestChange = "governance"
	// DigestChangePipeline lets the normal release pipeline change it, which
	// puts the supervisor inside the candidate's reach.
	DigestChangePipeline DigestChange = "pipeline"
)

// Valid reports whether c is a known rule.
func (c DigestChange) Valid() bool { return c == DigestChangeGovernance || c == DigestChangePipeline }

// HumanApproved reports whether changing the digest needs a person.
func (c DigestChange) HumanApproved() bool { return c == DigestChangeGovernance }

// Supervisor declares the trusted release supervisor. It belongs to the same
// deployment yet stays outside the candidate's modifiable area, which holds
// only when all four fields below say so; otherwise self-update is disabled.
type Supervisor struct {
	Image string `json:"image" yaml:"image"`
	// Digest pins the supervisor's own image, which is not built from the
	// same artifact as any candidate.
	Digest core.Digest `json:"digest" yaml:"digest"`
	// TrustVolume holds the last known good release, the signing keys and the
	// rollback configuration. Zone 1, Zone 2 and agent workspaces cannot
	// write to it.
	TrustVolume string `json:"trust_volume" yaml:"trust_volume"`
	// RootKeyRef locates the root public key the signed manifest is checked with.
	RootKeyRef   string       `json:"root_key_ref" yaml:"root_key_ref"`
	DigestChange DigestChange `json:"digest_change" yaml:"digest_change"`
	// StartupSelfCheck makes the supervisor refuse to run when its digest
	// deviates from the signed manifest.
	StartupSelfCheck bool `json:"startup_self_check" yaml:"startup_self_check"`
}

// StateDriver is the engine of the operational store.
type StateDriver string

const (
	StateSQLite   StateDriver = "sqlite"
	StatePostgres StateDriver = "postgres"
)

// Valid reports whether d is a known driver.
func (d StateDriver) Valid() bool { return d == StateSQLite || d == StatePostgres }

// ArtifactsKind is where artifacts, logs and snapshots are kept.
type ArtifactsKind string

const (
	ArtifactsVolume      ArtifactsKind = "volume"
	ArtifactsObjectStore ArtifactsKind = "object_store"
)

// Valid reports whether k is a known kind.
func (k ArtifactsKind) Valid() bool { return k == ArtifactsVolume || k == ArtifactsObjectStore }

// Storage declares the two durable units.
type Storage struct {
	State     StateStore    `json:"state" yaml:"state"`
	Artifacts ArtifactStore `json:"artifacts" yaml:"artifacts"`
}

// StateStore configures the operational store. Connection secrets are
// referenced, never written in the file.
type StateStore struct {
	Driver StateDriver `json:"driver" yaml:"driver"`
	DSNRef string      `json:"dsn_ref,omitempty" yaml:"dsn_ref,omitempty"`
	Path   string      `json:"path,omitempty" yaml:"path,omitempty"`
}

// ArtifactStore configures artifact storage.
type ArtifactStore struct {
	Kind     ArtifactsKind `json:"kind" yaml:"kind"`
	Location string        `json:"location" yaml:"location"`
}

// AdapterRef selects a registered adapter by name.
type AdapterRef struct {
	Name   string         `json:"name" yaml:"name"`
	Config map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
}

// Adapters selects the backends of the deployment. Coder and QA name
// different agent identities.
type Adapters struct {
	Coder     AdapterRef `json:"coder" yaml:"coder"`
	QA        AdapterRef `json:"qa" yaml:"qa"`
	Execution AdapterRef `json:"execution" yaml:"execution"`
	Deploy    AdapterRef `json:"deploy" yaml:"deploy"`
}

// Strategy is how a release reaches traffic.
type Strategy string

const (
	StrategyBlueGreen Strategy = "blue_green"
	StrategyCanary    Strategy = "canary"
)

// Valid reports whether s is a known strategy.
func (s Strategy) Valid() bool { return s == StrategyBlueGreen || s == StrategyCanary }

// Release is the release policy.
type Release struct {
	// Policy is how much agents may do without a person.
	Policy   core.DelegationMode `json:"policy" yaml:"policy"`
	Strategy Strategy            `json:"strategy" yaml:"strategy"`
	// CanarySteps are traffic percentages, used only with a traffic split.
	CanarySteps []int `json:"canary_steps,omitempty" yaml:"canary_steps,omitempty"`
	// Units lists the units the factory may publish by itself.
	Units      []UnitName `json:"units" yaml:"units"`
	Rollback   Rollback   `json:"rollback" yaml:"rollback"`
	LowTraffic LowTraffic `json:"low_traffic" yaml:"low_traffic"`
}

// Rollback holds the automatic rollback thresholds. A rollback needs the
// error rate and its rise over baseline both exceeded, on enough samples, for
// the given number of consecutive windows. The values are starting points to
// calibrate, and a candidate cannot change them for itself.
type Rollback struct {
	ErrorRate          float64  `json:"error_rate" yaml:"error_rate"`
	BaselineDelta      float64  `json:"baseline_delta" yaml:"baseline_delta"`
	MinSamples         int      `json:"min_samples" yaml:"min_samples"`
	Window             Duration `json:"window" yaml:"window"`
	ConsecutiveWindows int      `json:"consecutive_windows" yaml:"consecutive_windows"`
}

// LowTraffic configures evaluation when real traffic stays below MinSamples.
// Without a synthetic suite the app may not self-publish in that mode.
type LowTraffic struct {
	MinHold        Duration `json:"min_hold" yaml:"min_hold"`
	SyntheticSuite string   `json:"synthetic_suite,omitempty" yaml:"synthetic_suite,omitempty"`
}

// BudgetPeriod is when a capped budget resets.
type BudgetPeriod string

const (
	PeriodOneTime BudgetPeriod = "one_time"
	PeriodMonthly BudgetPeriod = "monthly"
)

// Valid reports whether p is a known period.
func (p BudgetPeriod) Valid() bool { return p == PeriodOneTime || p == PeriodMonthly }

// Budget is the Owner's spending policy. Amounts are in micros of the ledger
// currency; actual costs live in the ledger, not here.
type Budget struct {
	Mode core.BudgetMode `json:"mode" yaml:"mode"`
	// CapMicros applies only in capped mode.
	CapMicros core.Micros `json:"cap_micros,omitempty" yaml:"cap_micros,omitempty"`
	// AlertThresholdMicros triggers a notification, never a stop. AlertOff
	// records that the Owner explicitly chose to have no alert.
	AlertThresholdMicros core.Micros  `json:"alert_threshold_micros,omitempty" yaml:"alert_threshold_micros,omitempty"`
	AlertOff             bool         `json:"alert_off,omitempty" yaml:"alert_off,omitempty"`
	Period               BudgetPeriod `json:"period,omitempty" yaml:"period,omitempty"`
	// RollbackReserveMicros is kept back so a rollback can always be paid for.
	RollbackReserveMicros core.Micros `json:"rollback_reserve_micros,omitempty" yaml:"rollback_reserve_micros,omitempty"`
}

// Limits bound every task whatever the budget mode, so that an unlimited
// budget never means an unbounded loop.
type Limits struct {
	RunTimeout  Duration `json:"run_timeout" yaml:"run_timeout"`
	MaxAttempts int      `json:"max_attempts" yaml:"max_attempts"`
	Concurrency int      `json:"concurrency" yaml:"concurrency"`
	// ClarifyTimeout is how long an unanswered clarifying question waits.
	ClarifyTimeout Duration `json:"clarify_timeout" yaml:"clarify_timeout"`
}

// ModelRoutes maps each kind of step to the model it runs on. Code reads this
// table; a model is never asked to choose its own route.
type ModelRoutes struct {
	Routes map[core.StepKind]core.ModelRoute `json:"routes" yaml:"routes"`
	// Escalation lists, per step kind, the routes to try in order after a
	// run falls short. MaxEscalations caps how many are used per issue.
	Escalation     map[core.StepKind][]core.ModelRoute `json:"escalation,omitempty" yaml:"escalation,omitempty"`
	MaxEscalations int                                 `json:"max_escalations,omitempty" yaml:"max_escalations,omitempty"`
	// SameModelRoute is the Owner's acknowledgement that this deployment has
	// a single model route, so QA runs on the provider and model of the
	// coding agent and shares its blind spots. It is false unless stated;
	// without it such a route table is refused. Every QA receipt of the
	// deployment then records same_model_route, and policy may require the
	// Owner's approval in place of self-publishing.
	SameModelRoute bool `json:"same_model_route,omitempty" yaml:"same_model_route,omitempty"`
}

package manifest

import (
	"reflect"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Patterns of the text forms the manifest uses. They mirror the validators of
// the types they describe; the schema tests hold the two together.
const (
	idPattern       = `^[a-z0-9][a-z0-9._-]{0,62}$`
	digestPattern   = `^(sha256:[a-f0-9]{64}|sha512:[a-f0-9]{128})$`
	durationPattern = `^(0|([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+)$`
	ownerPattern    = `^[A-Za-z0-9][A-Za-z0-9-]*$`
	repoPattern     = `^[A-Za-z0-9._-]+$`
	namePattern     = `^[a-z0-9][a-z0-9._-]*$`
	hostPattern     = `^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`
)

// secretRefField is the schema of a field that names a secret. A connection
// string, a token or any other literal secret does not fit the pattern, so
// secrets stay out of the file.
var secretRefField = map[string]any{"pattern": secretRefPattern}

// enum is the schema of a string that takes one of a closed set of values.
func enum[T ~string](values ...T) map[string]any {
	names := make([]string, len(values))
	for i, v := range values {
		names[i] = string(v)
	}
	return map[string]any{"type": "string", "enum": names}
}

// text is the schema of a string that must match pattern.
func text(pattern string) map[string]any {
	return map[string]any{"type": "string", "pattern": pattern}
}

// names converts typed names to the plain strings a schema lists.
func names[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

// scalarSchemas maps each named scalar type to its schema: the closed value
// sets and text forms that a bare "string" or "integer" would lose.
func scalarSchemas() map[reflect.Type]map[string]any {
	zones := append(core.Zones(), core.ZoneShared)
	return map[reflect.Type]map[string]any{
		reflect.TypeOf(Mode("")):                enum(ModeStandalone, ModeDocker),
		reflect.TypeOf(UnitName("")):            enum(UnitNames()...),
		reflect.TypeOf(NetworkName("")):         enum(NetworkNames()...),
		reflect.TypeOf(Lifecycle("")):           enum(LifecycleLongRunning, LifecycleEphemeral, LifecycleDurable),
		reflect.TypeOf(DigestChange("")):        enum(DigestChangeGovernance, DigestChangePipeline),
		reflect.TypeOf(StateDriver("")):         enum(StateSQLite, StatePostgres),
		reflect.TypeOf(ArtifactsKind("")):       enum(ArtifactsVolume, ArtifactsObjectStore),
		reflect.TypeOf(Strategy("")):            enum(StrategyBlueGreen, StrategyCanary),
		reflect.TypeOf(BudgetPeriod("")):        enum(PeriodOneTime, PeriodMonthly),
		reflect.TypeOf(core.Zone("")):           enum(zones...),
		reflect.TypeOf(core.DelegationMode("")): enum(core.DelegationOnDemand, core.DelegationProposal, core.DelegationBounded),
		reflect.TypeOf(core.BudgetMode("")):     enum(core.BudgetUnlimited, core.BudgetCapped),
		reflect.TypeOf(core.EgressMode("")):     enum(core.EgressAllowlist, core.EgressUnrestricted),
		reflect.TypeOf(core.StepKind("")):       enum(core.StepKinds()...),
		reflect.TypeOf(core.FactoryID("")):      text(idPattern),
		reflect.TypeOf(core.DeploymentID("")):   text(idPattern),
		reflect.TypeOf(core.Digest("")):         text(digestPattern),
		reflect.TypeOf(Duration(0)):             text(durationPattern),
		reflect.TypeOf(core.Micros(0)):          {"type": "integer", "minimum": 0},
	}
}

// nonEmpty requires a string to be present, not merely declared.
var nonEmpty = map[string]any{"minLength": 1}

// atLeastOne is the lower bound of a count that must be positive.
var atLeastOne = map[string]any{"minimum": 1}

// described adds editor help text to a property.
func described(doc string, rule map[string]any) map[string]any {
	out := map[string]any{"description": doc}
	for key, value := range rule {
		out[key] = value
	}
	return out
}

// fieldRules holds, per "Type.key", the keywords merged over the schema that
// the field's Go type produced.
var fieldRules = map[string]map[string]any{
	"Manifest.api_version":   described("Manifest version; always "+APIVersion+".", map[string]any{"const": APIVersion}),
	"Manifest.factory_id":    described("Identity of the deployment for its whole life. All three zones share it.", nil),
	"Manifest.deployment_id": described("Name of this installation of the factory. It differs from factory_id and from any run identifier.", nil),
	"Manifest.mode":          described("How the units are realized: supervised processes on one machine, or one compose project.", nil),
	"Manifest.components":    described("The three zone components. A deployment missing one is not a factory deployment.", nil),
	"Manifest.units": described("The nine runtime units, by name. All must be declared.",
		map[string]any{"required": names(UnitNames())}),
	"Manifest.supervisor": described("The trusted release supervisor. Leaving it out disables self-update.", nil),
	"Manifest.networks": described("The four network segments. All must be declared.",
		map[string]any{"required": names(NetworkNames())}),
	"Manifest.volumes": described("Named places for files; each states the only zone allowed to write to it.",
		map[string]any{"patternProperties": map[string]any{namePattern: map[string]any{"$ref": "#/$defs/Volume"}}, "additionalProperties": false}),
	"Manifest.release": described("Release policy: delegation mode, switching strategy and automatic rollback thresholds.", nil),
	"Manifest.budget":  described("Spending policy set by the Owner. Measured costs live in the ledger, never here.", nil),
	"Manifest.limits":  described("Bounds on every task, whatever the budget mode.", nil),
	"Manifest.egress":  described("Outbound network policy of agent sandboxes.", nil),
	"Manifest.trusted_code_only": described(
		"Owner's acknowledgement that the execution backend can neither isolate agent runs nor enforce the egress policy, so the deployment runs only code the Owner trusts. Without it such a backend is refused.", nil),
	"Manifest.model_routes": described("Model per kind of step. Code reads this table; a model never picks its own route.", nil),
	"Manifest.extensions":   described("Settings these types do not model yet. Nothing here can grant a permission or relax a check.", nil),

	"Repository.owner": {"pattern": ownerPattern},
	"Repository.repo":  {"pattern": repoPattern},

	"Component.source":          described("Directory of the zone's code in the repository.", nonEmpty),
	"Component.credentials_ref": described("Reference to the credential set only this zone may use, written env:NAME or local:path/name; no two zones share one.", secretRefField),
	"Component.store_role":      described("Role this zone opens the state store with; no two zones share one.", nonEmpty),

	"Unit.port":     {"minimum": 1, "maximum": 65535},
	"Unit.networks": {"uniqueItems": true},
	"Unit.credentials_ref": described(
		"Reference to a credential set of the unit's own, written env:NAME or local:path/name. The qa-runner names one that differs from the runner's.", secretRefField),
	"Volume.holder": described("Unit that keeps a shared volume. Left out, artifacts holds the volume storage.artifacts names and state holds any other.",
		map[string]any{"enum": names(sharedUnits())}),
	"Mount.volume": nonEmpty,
	"Mount.path":   nonEmpty,

	"Supervisor.image":        described("The supervisor's own artifact; never the artifact of a candidate.", nonEmpty),
	"Supervisor.digest":       described("Pins the supervisor's artifact.", nil),
	"Supervisor.trust_volume": described("Volume holding the last known good release, the signing keys and the rollback configuration. Only the supervisor writes it.", nonEmpty),
	"Supervisor.root_key_ref": described("Reference to the root public key that verifies the signed manifest, written env:NAME or local:path/name.", secretRefField),
	"Supervisor.digest_change": described(
		"Who may change the pinned digest. Only \"governance\", a cycle approved by a person, keeps the supervisor out of a candidate's reach.", nil),
	"Supervisor.startup_self_check": described("Makes the supervisor refuse to run when its digest deviates from the signed manifest.", nil),

	"StateStore.dsn_ref":     described("Reference to the connection secret, written env:NAME or local:path/name. The connection string itself never goes in this file.", secretRefField),
	"ArtifactStore.location": nonEmpty,
	"AdapterRef.name":        {"pattern": `^[a-z0-9][a-z0-9_-]*$`},

	"Release.canary_steps":         {"items": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}},
	"Release.units":                described("Units the factory may publish by itself. The supervisor is never one of them.", map[string]any{"uniqueItems": true}),
	"Rollback.error_rate":          {"exclusiveMinimum": 0, "maximum": 1},
	"Rollback.baseline_delta":      {"minimum": 0, "maximum": 1},
	"Rollback.min_samples":         atLeastOne,
	"Rollback.consecutive_windows": atLeastOne,

	"Limits.max_attempts": atLeastOne,
	"Limits.concurrency":  atLeastOne,

	"EgressPolicy.allow":          {"items": text(hostPattern)},
	"ModelRoute.provider":         nonEmpty,
	"ModelRoute.model":            nonEmpty,
	"ModelRoutes.max_escalations": {"minimum": 0},
	"ModelRoutes.same_model_route": described(
		"Owner's acknowledgement that QA runs on the coding agent's provider and model. Without it such a route table is refused; with it every QA receipt records same_model_route.", nil),
}

// typeRules holds, per struct type, the keywords merged over its object
// schema: entries that are mandatory although their Go field is a pointer,
// and requirements that depend on the value of another field.
var typeRules = map[string]map[string]any{
	"Components": {"required": names(core.Zones())},
	"StateStore": {"allOf": []any{
		when("driver", string(StateSQLite), "path"),
		when("driver", string(StatePostgres), "dsn_ref"),
	}},
	// A capped budget needs a cap: the default is unlimited only when stated.
	"Budget": {"allOf": []any{when("mode", string(core.BudgetCapped), "cap_micros")}},
}

// when builds "if key has value, then needed must be present and not zero".
func when(key, value, needed string) map[string]any {
	return map[string]any{
		"if":   map[string]any{"properties": map[string]any{key: map[string]any{"const": value}}, "required": []string{key}},
		"then": map[string]any{"required": []string{needed}},
	}
}

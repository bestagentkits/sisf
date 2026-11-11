package conformance

// Rule names one conformance rule. Names are stable so that people, tools
// and tests can refer to a rule without matching message text.
type Rule string

const (
	// RuleSchema: the manifest departs from the published schema.
	RuleSchema Rule = "schema.invalid"

	// RuleZoneMissing: one of the three zones has no component.
	RuleZoneMissing Rule = "zone.missing"
	// RuleUnitMissing: one of the nine runtime units is not declared.
	RuleUnitMissing Rule = "unit.missing"
	// RuleUnitLifecycle: a unit declares a lifecycle other than the one its
	// name always has.
	RuleUnitLifecycle Rule = "unit.lifecycle_mismatch"
	// RuleUnitNotPinned: a unit that runs an artifact does not pin its digest.
	RuleUnitNotPinned Rule = "unit.digest_not_pinned"
	// RuleUnitThroughLauncher: a unit identified by what it starts starts
	// through a shell or another launcher, which hides what runs.
	RuleUnitThroughLauncher Rule = "unit.command_through_launcher"
	// RuleNetworkMissing: one of the four network segments is not declared.
	RuleNetworkMissing Rule = "network.missing"

	// RuleIdentityInvalid: factory_id or deployment_id is not a usable identifier.
	RuleIdentityInvalid Rule = "identity.invalid"
	// RuleIdentityNotDistinct: the factory, the installation and the runtime
	// units cannot be told apart by their identifiers.
	RuleIdentityNotDistinct Rule = "identity.not_distinct"

	// RuleSupervisorUndeclared: the factory is asked to publish by itself
	// although no trusted supervisor is declared.
	RuleSupervisorUndeclared Rule = "supervisor.undeclared"
	// RuleSupervisorNotPinned: the supervisor does not run from its own
	// digest-pinned artifact (boundary condition 1).
	RuleSupervisorNotPinned Rule = "supervisor.digest_not_pinned"
	// RuleSupervisorSharesArtifact: the supervisor is built from the same
	// artifact as a unit a candidate can replace (boundary condition 1).
	RuleSupervisorSharesArtifact Rule = "supervisor.shares_candidate_artifact"
	// RuleTrustVolumeUndeclared: the volume for the last known good release,
	// the signing keys and the rollback configuration is not declared
	// (boundary condition 2).
	RuleTrustVolumeUndeclared Rule = "supervisor.trust_volume_undeclared"
	// RuleTrustVolumeExposed: something other than the supervisor can write
	// that volume, or a unit outside Zone 3 can reach it (boundary condition 2).
	RuleTrustVolumeExposed Rule = "supervisor.trust_volume_exposed"
	// RuleTrustVolumeUnmounted: the supervisor does not hold that volume
	// writable, so what it must protect lives elsewhere (boundary condition 2).
	RuleTrustVolumeUnmounted Rule = "supervisor.trust_volume_unmounted"
	// RuleSupervisorDigestChange: the normal release pipeline may change the
	// supervisor's digest (boundary condition 3).
	RuleSupervisorDigestChange Rule = "supervisor.digest_change_by_pipeline"
	// RuleSupervisorSelfCheck: the supervisor does not check its own digest
	// against the signed manifest at startup (boundary condition 4).
	RuleSupervisorSelfCheck Rule = "supervisor.self_check_missing"
	// RuleTrustVolumeStorage: the state store or the artifact store is kept
	// on the trust volume, where what agents produce would sit beside the
	// signing keys (boundary condition 2).
	RuleTrustVolumeStorage Rule = "supervisor.trust_volume_used_as_storage"

	// RuleSharedCredentials: two zones use the same credential set.
	RuleSharedCredentials Rule = "isolation.shared_credentials"
	// RuleSharedStoreRole: two zones open the state store with the same role.
	RuleSharedStoreRole Rule = "isolation.shared_store_role"
	// RuleVolumeUndeclared: a unit mounts a volume no zone owns.
	RuleVolumeUndeclared Rule = "isolation.volume_undeclared"
	// RuleCrossZoneWrite: a unit can write a volume owned by another zone,
	// such as Zone 1 writing into Zone 3.
	RuleCrossZoneWrite Rule = "isolation.cross_zone_write"
	// RuleCrossZoneRead: a unit of Zone 1 or Zone 2 mounts, read-only, a
	// volume owned by another zone.
	RuleCrossZoneRead Rule = "isolation.cross_zone_read"
	// RuleSharedVolumeMount: a volume marked shared is mounted by a unit
	// other than the durable unit that holds it.
	RuleSharedVolumeMount Rule = "isolation.shared_volume_mount"
	// RuleNetworkSegment: a unit is attached to a segment outside the set
	// its role allows.
	RuleNetworkSegment Rule = "isolation.network_segment"
	// RuleNetworkUnattached: a unit that runs is attached to no segment, so
	// the platform would place it on a network this manifest does not divide.
	RuleNetworkUnattached Rule = "isolation.network_unattached"
	// RuleSandboxNotInternal: the sandbox segment has a route to the outside
	// of its own.
	RuleSandboxNotInternal Rule = "isolation.sandbox_not_internal"
	// RuleEgressProxy: a segment's way out is missing or is not a declared
	// unit attached to that segment.
	RuleEgressProxy Rule = "isolation.egress_proxy_undeclared"
	// RulePublicExposure: a unit is exposed outside the deployment although
	// its role does not allow it, or the control unit is exposed whole.
	RulePublicExposure Rule = "isolation.public_exposure"

	// RuleVolumeHolder: a volume names a holder although a zone owns it, or
	// names a unit that is not one every zone uses.
	RuleVolumeHolder Rule = "isolation.volume_holder_invalid"
	// RuleVolumePathOverlap: on one machine, the directories of two volumes
	// with different owners are the same directory or lie inside each other.
	RuleVolumePathOverlap Rule = "isolation.volume_path_overlap"
	// RuleVolumeDirectories: on one machine, one volume is mounted at two
	// directories, so what the manifest says of the volume holds for neither.
	RuleVolumeDirectories Rule = "isolation.volume_mounted_at_two_directories"
	// RuleExecutableInAnotherUnitsDirectory: on one machine, a unit starts a
	// binary from a directory another unit mounts, so that unit decides what
	// runs.
	RuleExecutableInAnotherUnitsDirectory Rule = "isolation.executable_in_another_units_directory"
	// RuleRunnerInbound: a runner declares a port, although a run accepts no
	// inbound connection.
	RuleRunnerInbound Rule = "isolation.runner_inbound_port"

	// RuleQARouteMissing: the coding step or the QA step has no model route.
	RuleQARouteMissing Rule = "qa.model_route_missing"
	// RuleQASameModelRoute: QA runs on the provider and model of the coding
	// agent and the Owner has not acknowledged it.
	RuleQASameModelRoute Rule = "qa.same_model_route"
	// RuleQASharedWorkspace: the runner and the qa-runner mount one volume.
	RuleQASharedWorkspace Rule = "qa.shared_workspace"
	// RuleQASharedCredentials: the qa-runner has no credential set different
	// from the runner's.
	RuleQASharedCredentials Rule = "qa.shared_credentials"

	// RuleStatePlacement: the state store file lies outside the volume the
	// state unit holds.
	RuleStatePlacement Rule = "storage.state_path_misplaced"
	// RuleArtifactsPlacement: artifacts kept on a volume are not on a shared
	// volume the artifacts unit holds.
	RuleArtifactsPlacement Rule = "storage.artifact_location_misplaced"

	// RuleSecretInline: a setting that holds a secret carries a value where
	// a reference belongs.
	RuleSecretInline Rule = "secret.inline_value"
	// RuleSecretRefReused: one reference stands for two secrets that must
	// differ, such as a zone's credentials and the state connection secret.
	RuleSecretRefReused Rule = "secret.reference_reused"

	// RuleModeUnsupported: the operating mode has no supported profile.
	RuleModeUnsupported Rule = "mode.unsupported"
	// RuleBackendUndeclared: a selected backend is not in the capability matrix.
	RuleBackendUndeclared Rule = "backend.undeclared"
	// RuleBackendMode: a selected backend does not run in the operating mode.
	RuleBackendMode Rule = "backend.mode_mismatch"
	// RuleBackendCapability: the manifest asks for something the selected
	// backend declares it cannot do.
	RuleBackendCapability Rule = "backend.capability_mismatch"

	// RuleLimitsUnbounded: a task could run without a bound on time,
	// attempts or concurrency.
	RuleLimitsUnbounded Rule = "limits.unbounded"

	// RuleReleaseDuration: a span a release is observed over is zero, so the
	// observation it stands for never happens.
	RuleReleaseDuration Rule = "release.duration_not_positive"
	// RuleCanaryStepsMissing: a canary release names no traffic steps.
	RuleCanaryStepsMissing Rule = "release.canary_steps_missing"
	// RuleCanarySteps: the canary steps do not grow from a first step below
	// all traffic.
	RuleCanarySteps Rule = "release.canary_steps_not_increasing"
	// RuleCanaryIncomplete: the last canary step is not all of the traffic,
	// so the release never finishes.
	RuleCanaryIncomplete Rule = "release.canary_steps_incomplete"
	// RuleReleaseBounds: a release setting is well formed yet so small or so
	// large that the check it configures never decides anything.
	RuleReleaseBounds Rule = "release.setting_out_of_range"
	// RuleSyntheticSuiteMissing: the factory publishes by itself although no
	// synthetic suite exists to judge a release that gets little traffic.
	RuleSyntheticSuiteMissing Rule = "release.synthetic_suite_missing"
	// RuleReleaseUnit: release.units lists a unit the normal pipeline may
	// not publish by itself.
	RuleReleaseUnit Rule = "release.unit_not_self_publishable"
	// RuleBudgetAlert: the budget is unlimited and the Owner has neither set
	// an alert threshold nor chosen to have none, or an alert threshold is
	// set and declined at once.
	RuleBudgetAlert Rule = "budget.alert_undecided"
	// RuleModelRoutesEmpty: no kind of step has a model route.
	RuleModelRoutesEmpty Rule = "model_routes.empty"
)

// Rules returns every rule the checker can report.
func Rules() []Rule {
	return []Rule{
		RuleSchema, RuleZoneMissing, RuleUnitMissing, RuleUnitLifecycle, RuleUnitNotPinned, RuleUnitThroughLauncher, RuleNetworkMissing,
		RuleIdentityInvalid, RuleIdentityNotDistinct,
		RuleSupervisorUndeclared, RuleSupervisorNotPinned, RuleSupervisorSharesArtifact,
		RuleTrustVolumeUndeclared, RuleTrustVolumeExposed, RuleTrustVolumeUnmounted,
		RuleSupervisorDigestChange, RuleSupervisorSelfCheck, RuleTrustVolumeStorage,
		RuleSharedCredentials, RuleSharedStoreRole, RuleVolumeUndeclared, RuleCrossZoneWrite, RuleCrossZoneRead,
		RuleSharedVolumeMount, RuleNetworkSegment, RuleNetworkUnattached, RuleSandboxNotInternal, RuleEgressProxy,
		RulePublicExposure, RuleVolumeHolder, RuleVolumePathOverlap, RuleVolumeDirectories, RuleExecutableInAnotherUnitsDirectory, RuleRunnerInbound,
		RuleQARouteMissing, RuleQASameModelRoute, RuleQASharedWorkspace, RuleQASharedCredentials,
		RuleStatePlacement, RuleArtifactsPlacement, RuleSecretInline, RuleSecretRefReused,
		RuleModeUnsupported, RuleBackendUndeclared, RuleBackendMode, RuleBackendCapability,
		RuleLimitsUnbounded, RuleReleaseDuration, RuleCanaryStepsMissing, RuleCanarySteps, RuleCanaryIncomplete,
		RuleReleaseBounds, RuleSyntheticSuiteMissing, RuleReleaseUnit, RuleBudgetAlert, RuleModelRoutesEmpty,
	}
}

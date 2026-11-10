// Package conformance decides whether a factory manifest declares one
// conformant factory deployment.
//
// A manifest is conformant when it declares all three zones and all nine
// runtime units under one identity, keeps the zones isolated from each other,
// places the trusted release supervisor outside any candidate's reach, and
// selects only backends able to do what it asks of them. Every rule that
// fails yields a finding that names the rule, the place in the manifest and
// the change that fixes it. The checker reads declarations only: it is the
// gate before a deployment starts, not proof of what runs afterwards.
package conformance

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/internal/core"
)

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

	// RuleSharedCredentials: two zones use the same credential set.
	RuleSharedCredentials Rule = "isolation.shared_credentials"
	// RuleSharedStoreRole: two zones open the state store with the same role.
	RuleSharedStoreRole Rule = "isolation.shared_store_role"
	// RuleVolumeUndeclared: a unit mounts a volume no zone owns.
	RuleVolumeUndeclared Rule = "isolation.volume_undeclared"
	// RuleCrossZoneWrite: a unit can write a volume owned by another zone,
	// such as Zone 1 writing into Zone 3.
	RuleCrossZoneWrite Rule = "isolation.cross_zone_write"
	// RuleNetworkSegment: a unit is attached to a segment of another zone.
	RuleNetworkSegment Rule = "isolation.network_segment"

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
)

// Rules returns every rule the checker can report.
func Rules() []Rule {
	return []Rule{
		RuleSchema, RuleZoneMissing, RuleUnitMissing, RuleUnitLifecycle, RuleNetworkMissing,
		RuleIdentityInvalid, RuleIdentityNotDistinct,
		RuleSupervisorUndeclared, RuleSupervisorNotPinned, RuleSupervisorSharesArtifact,
		RuleTrustVolumeUndeclared, RuleTrustVolumeExposed, RuleTrustVolumeUnmounted,
		RuleSupervisorDigestChange, RuleSupervisorSelfCheck,
		RuleSharedCredentials, RuleSharedStoreRole, RuleVolumeUndeclared, RuleCrossZoneWrite, RuleNetworkSegment,
		RuleModeUnsupported, RuleBackendUndeclared, RuleBackendMode, RuleBackendCapability,
		RuleLimitsUnbounded,
	}
}

// Finding is one way a manifest fails to be conformant.
type Finding struct {
	Rule Rule `json:"rule"`
	// Path locates the offending value in manifest keys, for example
	// "units.app.mounts[0]".
	Path string `json:"path"`
	// Message says what is wrong and why it matters.
	Message string `json:"message"`
	// Fix says what to change in the manifest.
	Fix string `json:"fix,omitempty"`
}

// Report is the outcome of checking one manifest.
type Report struct {
	// Source names the file that was checked, when there was one.
	Source   string    `json:"source,omitempty"`
	Findings []Finding `json:"findings"`
	// SelfUpdate reports whether a trusted supervisor is declared and all
	// four conditions of its boundary hold. When it is false the factory
	// must not publish by itself.
	SelfUpdate bool `json:"self_update"`
}

// Conformant reports whether no rule failed.
func (r Report) Conformant() bool { return len(r.Findings) == 0 }

// Has reports whether rule failed at least once.
func (r Report) Has(rule Rule) bool {
	return slices.ContainsFunc(r.Findings, func(f Finding) bool { return f.Rule == rule })
}

// Err returns nil for a conformant manifest. Otherwise it returns a typed
// error that counts the findings, names the rules that failed and says what
// the caller may do next.
func (r Report) Err() error {
	if r.Conformant() {
		return nil
	}
	var failed []string
	for _, finding := range r.Findings {
		if !slices.Contains(failed, string(finding.Rule)) {
			failed = append(failed, string(finding.Rule))
		}
	}
	sort.Strings(failed)
	subject, next := "the manifest", core.NextAction{
		Action:      "conformance.check",
		Description: "correct each finding as its fix describes, then check the manifest again",
	}
	if r.Source != "" {
		subject = "manifest " + r.Source
		next.Params = map[string]string{"file": r.Source}
	}
	noun := "findings"
	if len(r.Findings) == 1 {
		noun = "finding"
	}
	failure := core.NewError(core.CodeInvalidArgument,
		fmt.Sprintf("%s is not conformant: %d %s", subject, len(r.Findings), noun), next)
	failure.Details = map[string]string{"findings": strconv.Itoa(len(r.Findings)), "rules": strings.Join(failed, ",")}
	return failure
}

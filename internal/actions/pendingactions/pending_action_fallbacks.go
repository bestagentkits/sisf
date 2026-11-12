// Package pendingactions lists the actions of the query list whose
// implementation lives in a package that is not wired into this factory:
// budget, secret refs, the product catalog and release rollback. Each is a
// fallback that answers NOT_SUPPORTED with next_actions, so every surface
// lists the action and refuses honestly instead of faking a result.
//
// Register them with Registry.RegisterFallback. When the owning package
// registers the real action under the same name, it replaces the fallback.
package pendingactions

import (
	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Names of the actions this package stands in for.
const (
	ActionBudgetGet              = "budget.get"
	ActionBudgetSet              = "budget.set"
	ActionSecretsListRefs        = "secrets.list_refs"
	ActionFeaturesList           = "features.list"
	ActionChangelogList          = "changelog.list"
	ActionDocsList               = "docs.list"
	ActionDecisionsProposeUpdate = "decisions.propose_update"
	ActionDeploymentsRollback    = "deployments.rollback"
)

var (
	everyReader = []core.Role{core.RoleOwner, core.RoleMaintainer, core.RoleReporter, core.RoleViewer, core.RoleAgent}
	ownerOnly   = []core.Role{core.RoleOwner}
	maintainers = []core.Role{core.RoleOwner, core.RoleMaintainer}
	inspectable = []core.Role{core.RoleOwner, core.RoleMaintainer, core.RoleViewer}
)

const (
	emptyInput  = `{"type":"object","additionalProperties":false}`
	anyObject   = `{"type":"object"}`
	statusNext  = "factory.status"
	explainNext = "runs.explain"
)

type pending struct {
	spec   core.ActionSpec
	reason string
	next   core.NextAction
}

func catalog() []pending {
	status := core.NextAction{Action: statusNext, Description: "see what this factory can do now"}
	return []pending{
		{core.ActionSpec{Name: ActionBudgetGet, Summary: "Show the budget mode, cap, alert threshold and usage, with unverified usage marked.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"budget:read"}, Roles: inspectable,
			Input: actions.Schema(emptyInput), Output: actions.Schema(anyObject)},
			"the budget service is not wired into this factory", status},
		{core.ActionSpec{Name: ActionBudgetSet, Summary: "Change the budget mode, cap, alert threshold or rollback reserve; Owner only.",
			Sensitivity: core.SensitivitySensitive, Scopes: []string{"budget:write"}, Roles: ownerOnly, StepUp: true,
			Input: actions.Schema(anyObject), Output: actions.Schema(anyObject)},
			"the budget service is not wired into this factory", status},
		{core.ActionSpec{Name: ActionSecretsListRefs, Summary: "List secret references by provider, name, class and zone; never values.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"secrets:read"}, Roles: maintainers,
			Input: actions.Schema(emptyInput), Output: actions.Schema(anyObject)},
			"the secret-reference actions are not wired into this factory", status},
		{core.ActionSpec{Name: ActionFeaturesList, Summary: "List the feature catalog.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"catalog:read"}, Roles: everyReader,
			Input: actions.Schema(emptyInput), Output: actions.Schema(anyObject)},
			"the product catalog is not wired into this factory", status},
		{core.ActionSpec{Name: ActionChangelogList, Summary: "List the changelog entries of released work.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"catalog:read"}, Roles: everyReader,
			Input: actions.Schema(emptyInput), Output: actions.Schema(anyObject)},
			"the product catalog is not wired into this factory", status},
		{core.ActionSpec{Name: ActionDocsList, Summary: "List the docs, guides and ADRs of the product.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"catalog:read"}, Roles: everyReader,
			Input: actions.Schema(emptyInput), Output: actions.Schema(anyObject)},
			"the product catalog is not wired into this factory", status},
		{core.ActionSpec{Name: ActionDecisionsProposeUpdate, Summary: "Propose a change to DECISIONS.md as a pull request the Owner must approve; never applied directly.",
			Sensitivity: core.SensitivityWrite, Scopes: []string{"decisions:write"}, Roles: maintainers,
			Input: actions.Schema(anyObject), Output: actions.Schema(anyObject)},
			"the decision proposal service is not wired into this factory", status},
		{core.ActionSpec{Name: ActionDeploymentsRollback, Summary: "Roll a deployment back to its last known good release through the release supervisor.",
			Sensitivity: core.SensitivitySensitive, Scopes: []string{"deployments:write"}, Roles: maintainers, StepUp: true,
			Input: actions.Schema(anyObject), Output: actions.Schema(anyObject)},
			"the release supervisor offers no rollback request to the control zone yet; automatic rollback still applies",
			core.NextAction{Action: explainNext, Description: "see why a release was or was not rolled back"}},
	}
}

// Specs returns the specs of the actions this package stands in for.
func Specs() []core.ActionSpec {
	var out []core.ActionSpec
	for _, p := range catalog() {
		out = append(out, p.spec)
	}
	return out
}

// Fallbacks returns the NOT_SUPPORTED actions, for Registry.RegisterFallback.
func Fallbacks() []core.Action {
	var out []core.Action
	for _, p := range catalog() {
		out = append(out, actions.NotSupported(p.spec, p.reason, p.next))
	}
	return out
}

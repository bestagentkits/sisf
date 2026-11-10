// Package operationsactions serves the two operations the Owner runs on a
// live factory from any surface: factory.emergency_stop and
// deployments.rollback. Both go through the release supervisor's admin
// socket; the control zone never calls a deploy backend and holds no
// authority of its own to roll anything back. The supervisor verifies and
// spends the Owner's rollback approval itself.
package operationsactions

import (
	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Names of the actions this package serves.
const (
	ActionEmergencyStop = "factory.emergency_stop"
	ActionRollback      = "deployments.rollback"
)

const (
	stopInput = `{"type":"object","additionalProperties":false,"required":["reason"],"properties":{` +
		`"reason":{"type":"string","minLength":1,"maxLength":512,"description":"Why the factory is stopped, for the people who lift the stop."}}}`
	stopOutput = `{"type":"object","properties":{"stop_id":{"type":"string"},"stopped_at":{"type":"string","format":"date-time"},` +
		`"publishing_frozen":{"type":"boolean"},"claims_held":{"type":"array","items":{"type":"string"}},` +
		`"runs_cancelled":{"type":"array","items":{"type":"string"}},"secrets_revoked":{"type":"array","items":{"type":"string"}},` +
		`"failures":{"type":"array","items":{"type":"string"}}}}`
	rollbackInput = `{"type":"object","additionalProperties":false,"required":["reason"],"properties":{` +
		`"reason":{"type":"string","minLength":1,"maxLength":512},` +
		`"unit":{"type":"string","maxLength":512,"description":"Unit to restore; may be left out when exactly one unit has a last known good release."}}}`
	rollbackOutput = `{"type":"object","properties":{"unit":{"type":"string"},"from":{"type":"string"},"restored_to":{"type":"string"},` +
		`"observed":{"type":"string"},"healthy":{"type":"boolean"},"at":{"type":"string","format":"date-time"}}}`
)

// Specs returns the specs of the actions this package serves.
func Specs() []core.ActionSpec {
	return []core.ActionSpec{
		{Name: ActionEmergencyStop, Summary: "Stop the factory now: hold claims, cancel running runs, revoke their secrets and freeze publishing; Owner only.",
			Sensitivity: core.SensitivityWrite, Scopes: []string{"factory:stop"}, Roles: []core.Role{core.RoleOwner},
			Input: actions.Schema(stopInput), Output: actions.Schema(stopOutput)},
		{Name: ActionRollback, Summary: "Roll a deployment back to its last known good release through the release supervisor.",
			Sensitivity: core.SensitivitySensitive, Scopes: []string{"deployments:write"},
			Roles: []core.Role{core.RoleOwner, core.RoleMaintainer}, StepUp: true,
			Input: actions.Schema(rollbackInput), Output: actions.Schema(rollbackOutput)},
	}
}

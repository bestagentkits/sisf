// Package approvalactions serves the approval inbox the same way on every
// surface: approvals.list, approvals.get and approvals.decide. A decision is
// recorded by the approval store, which signs it and allows only an Owner
// who has just confirmed their identity.
package approvalactions

import (
	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Names of the actions this package serves.
const (
	ActionList   = "approvals.list"
	ActionGet    = "approvals.get"
	ActionDecide = actions.ApprovalDecisionAction
)

var inboxReaders = []core.Role{core.RoleOwner, core.RoleMaintainer, core.RoleViewer}

// Specs returns the specs of the actions this package serves.
func Specs() []core.ActionSpec {
	return []core.ActionSpec{
		{Name: ActionList, Summary: "List approval requests, oldest first, with their one-line risk summary.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"approvals:read"}, Roles: inboxReaders,
			Input: actions.Schema(`{"type":"object","additionalProperties":false,"properties":{
"statuses":{"type":"array","items":{"type":"string","enum":["pending","approved","rejected","changes_requested","expired","consumed"]}},
"issue":{"type":"string","description":"Issue reference owner/repo#number."},
"limit":{"type":"integer","minimum":1,"maximum":500}}}`),
			Output: actions.Schema(`{"type":"object","properties":{"requests":{"type":"array","items":{"type":"object"}}}}`)},
		{Name: ActionGet, Summary: "Read one approval request: proposal, evidence, diff, affected features and its decision.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"approvals:read"}, Roles: inboxReaders,
			Input: actions.Schema(`{"type":"object","additionalProperties":false,"required":["id"],
"properties":{"id":{"type":"string"}}}`),
			Output: actions.Schema(`{"type":"object","properties":{"request":{"type":"object"},"decision":{"type":["object","null"]}}}`)},
		{Name: ActionDecide, Summary: "Approve, reject or request changes on an approval request; Owner only, with step-up.",
			Sensitivity: core.SensitivitySensitive, Scopes: []string{"approvals:write"}, Roles: []core.Role{core.RoleOwner}, StepUp: true,
			Input: actions.Schema(`{"type":"object","additionalProperties":false,
"required":["request_id","nonce","subject_digest","policy_revision","decision"],
"description":"nonce, subject_digest and policy_revision echo what the approver was shown.",
"properties":{"request_id":{"type":"string"},"nonce":{"type":"string"},"subject_digest":{"type":"string"},
"policy_revision":{"type":"string"},"decision":{"type":"string","enum":["approve","reject","request_changes"]},
"reason":{"type":"string"},
"conditions":{"type":"object","additionalProperties":false,"properties":{
"budget_cap":{"type":"integer","minimum":0,"description":"Spending cap in micros."},
"expires_at":{"type":"string","format":"date-time"}}}}}`),
			Output: actions.Schema(`{"type":"object","properties":{"request":{"type":"object"}}}`)},
	}
}

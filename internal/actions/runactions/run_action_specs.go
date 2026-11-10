// Package runactions serves the read actions about the factory, its issues
// and their runs: factory.status, issues.list, runs.inspect and runs.explain.
// They read the operational store, the dispatcher's run records and the
// approval inbox, and change nothing.
package runactions

import (
	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Names of the actions this package serves.
const (
	ActionFactoryStatus = "factory.status"
	ActionIssuesList    = "issues.list"
	ActionRunsInspect   = "runs.inspect"
	ActionRunsExplain   = "runs.explain"
)

var readers = []core.Role{core.RoleOwner, core.RoleMaintainer, core.RoleReporter, core.RoleViewer, core.RoleAgent}

const targetSchema = `{"type":"object","additionalProperties":false,
"description":"Exactly one of issue and run_id.",
"properties":{"issue":{"type":"string","description":"Issue reference owner/repo#number."},
"run_id":{"type":"string","description":"Run identifier."}}}`

// Specs returns the specs of the actions this package serves.
func Specs() []core.ActionSpec {
	return []core.ActionSpec{
		{Name: ActionFactoryStatus, Summary: "Overview of the factory: issues per lifecycle state, stopped issues and open approvals.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"factory:read"}, Roles: readers,
			Input: actions.Schema(`{"type":"object","additionalProperties":false}`),
			Output: actions.Schema(`{"type":"object","properties":{"factory_id":{"type":"string"},"generated_at":{"type":"string","format":"date-time"},
"issue_total":{"type":"integer"},"stopped":{"type":"integer"},"open_approvals":{"type":"integer"},
"issue_counts":{"type":"array","items":{"type":"object","properties":{"state":{"type":"string"},"count":{"type":"integer"}}}}}}`)},
		{Name: ActionIssuesList, Summary: "List tracked issues with their lifecycle record, most recently changed first.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"issues:read"}, Roles: readers,
			Input: actions.Schema(`{"type":"object","additionalProperties":false,"properties":{
"states":{"type":"array","items":{"type":"string"},"description":"Lifecycle states to keep; empty keeps all."},
"limit":{"type":"integer","minimum":1,"maximum":500}}}`),
			Output: actions.Schema(`{"type":"object","properties":{"total":{"type":"integer"},"issues":{"type":"array","items":{"type":"object"}}}}`)},
		{Name: ActionRunsInspect, Summary: "Show the lifecycle record of an issue and the dispatcher record of its current run.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"runs:read"}, Roles: readers,
			Input:  actions.Schema(targetSchema),
			Output: actions.Schema(`{"type":"object","properties":{"record":{"type":"object"},"run":{"type":["object","null"]}}}`)},
		{Name: ActionRunsExplain, Summary: "Explain why an issue's run is stopped or blocked, with evidence, its current state and the next valid steps.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"runs:read"}, Roles: readers,
			Input: actions.Schema(targetSchema),
			Output: actions.Schema(`{"type":"object","properties":{"issue":{"type":"string"},"state":{"type":"string"},"run_id":{"type":"string"},
"stopped":{"type":"boolean"},"cause":{"type":"string","enum":["blocked","rolled_back","awaiting_approval","awaiting_information","attempts_exhausted","finished","in_progress"]},
"reason":{"type":"string"},"evidence":{"type":"array","items":{"type":"object","properties":{"kind":{"type":"string"},"ref":{"type":"string"},"summary":{"type":"string"},"at":{"type":"string","format":"date-time"}}}},
"next_actions":{"type":"array","items":{"type":"object"}}}}`)},
	}
}

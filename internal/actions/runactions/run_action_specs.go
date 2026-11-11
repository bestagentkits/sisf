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
		{Name: ActionFactoryStatus, Summary: "Overview of the factory: issues per lifecycle state, stopped issues, open approvals and how it keeps up with GitHub.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"factory:read"}, Roles: readers,
			Input: actions.Schema(`{"type":"object","additionalProperties":false}`),
			Output: actions.Schema(`{"type":"object","properties":{"factory_id":{"type":"string"},"generated_at":{"type":"string","format":"date-time"},
"issue_total":{"type":"integer"},"stopped":{"type":"integer"},"open_approvals":{"type":"integer"},
"issue_counts":{"type":"array","items":{"type":"object","properties":{"state":{"type":"string"},"count":{"type":"integer"}}}},
"github":{"type":"object","description":"How the factory keeps up with GitHub: reconciliation of lost webhooks and the operator token's access.",
"properties":{"position":{"type":"string","format":"date-time"},"last_poll_at":{"type":"string","format":"date-time"},
"backlog":{"type":"boolean","description":"The last poll stopped at its bound; more changes wait for the next polls."},
"last_error":{"type":"string"},
"retrying":{"type":"array","items":{"type":"object","properties":{"issue":{"type":"integer"},"updated_at":{"type":"string","format":"date-time"},"attempts":{"type":"integer"},"reason":{"type":"string"}}}},
"skipped":{"type":"array","description":"Issue changes given up after repeated failures.","items":{"type":"object","properties":{"issue":{"type":"integer"},"updated_at":{"type":"string","format":"date-time"},"attempts":{"type":"integer"},"reason":{"type":"string"}}}},
"token_access":{"type":"string","enum":["confirmed","revoked"]},"token_access_detail":{"type":"string"},
"token_checked_at":{"type":"string","format":"date-time"},"parking_deferred_since":{"type":"string","format":"date-time"},"parking_deferred_reason":{"type":"string"},
"reports_waiting":{"type":"integer","description":"Bug reports from the public report route not yet published as GitHub issues for triage."},
"report_publish_error":{"type":"string","description":"Why the last publication of bug reports failed."}}}}}`)},
		{Name: ActionIssuesList, Summary: "List tracked issues with their lifecycle record, most recently changed first.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"issues:read"}, Roles: readers,
			Input: actions.Schema(`{"type":"object","additionalProperties":false,"properties":{
"states":{"type":"array","items":{"type":"string"},"description":"Lifecycle states to keep; empty keeps all."},
"limit":{"type":"integer","minimum":1,"maximum":500}}}`),
			Output: actions.Schema(`{"type":"object","properties":{"total":{"type":"integer"},"issues":{"type":"array","items":{"type":"object"}}}}`)},
		{Name: ActionRunsInspect, Summary: "Show the lifecycle record of an issue and the dispatcher record of its current run.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"runs:read"}, Roles: readers,
			Input: actions.Schema(targetSchema),
			Output: actions.Schema(`{"type":"object","properties":{"record":{"type":"object"},"run":{"type":["object","null"]},
"qa_hold":{"type":"object","description":"Present when QA held the run's candidate and the issue waits in QAEvaluation for the Owner."},
"pull_request":{"type":"object","description":"The pull request of the run's candidate QA passed: its number, URL and whether the supervisor merged it."},
"release_hold":{"type":"object","description":"Present when the supervisor held or refused the release of the candidate and the issue waits in ReleaseCandidate: the result, the code, the public summary, the supervisor's reason (to the Owner only) and whether the Owner's retry resumed it."}}}`)},
		{Name: ActionRunsExplain, Summary: "Explain why an issue's run is stopped or blocked, with evidence, its current state and the next valid steps.",
			Sensitivity: core.SensitivityRead, Scopes: []string{"runs:read"}, Roles: readers,
			Input: actions.Schema(targetSchema),
			Output: actions.Schema(`{"type":"object","properties":{"issue":{"type":"string"},"state":{"type":"string"},"run_id":{"type":"string"},
"stopped":{"type":"boolean"},"cause":{"type":"string","enum":["blocked","rolled_back","awaiting_approval","awaiting_information","attempts_exhausted","release_held","release_refused","finished","in_progress"]},
"reason":{"type":"string"},"evidence":{"type":"array","items":{"type":"object","properties":{"kind":{"type":"string"},"ref":{"type":"string"},"summary":{"type":"string"},"at":{"type":"string","format":"date-time"}}}},
"next_actions":{"type":"array","items":{"type":"object"}}}}`)},
	}
}

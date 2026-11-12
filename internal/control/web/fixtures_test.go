package web_test

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/pkg/core"
)

// hostileText is what an attacker could put in an issue title, a comment or
// a feature name: markup, an attribute breakout and a script.
const hostileText = `<script>alert('x')</script><img src=x onerror=alert(1)> "></a><script>alert(2)</script>`

// fixtures are the answers of the in-test actions.
type fixtures struct {
	hostile         string
	budgetConfirmed bool
	githubRef       bool
	sample          bool
}

var (
	everyone  = []core.Role{core.RoleOwner, core.RoleViewer}
	ownerOnly = []core.Role{core.RoleOwner}
	issueOne  = core.IssueRef{Owner: "acme", Repo: "app", Number: 1}
	issueTwo  = core.IssueRef{Owner: "acme", Repo: "app", Number: 2}
)

func readAction(name string, out func() any) core.Action {
	return core.Action{
		Spec: core.ActionSpec{Name: name, Summary: "Read " + name + ".", Sensitivity: core.SensitivityRead, Scopes: []string{"test:read"}, Roles: everyone},
		Handle: func(context.Context, core.ActionRequest) (core.ActionResult, error) {
			return actions.JSONResult(out())
		},
	}
}

func (f *fixtures) actions(s *seen) []core.Action {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	out := []core.Action{
		readAction("factory.status", func() any {
			return runactions.FactoryStatus{FactoryID: testFactory, GeneratedAt: now, IssueTotal: 2, Stopped: 1, OpenApprovals: 1,
				IssueCounts: []runactions.StateCount{{State: core.StateReady, Count: 1}, {State: core.StateBlocked, Count: 1}}}
		}),
		readAction("issues.list", func() any {
			return runactions.IssueList{Total: 2, Issues: []core.StateRecord{
				{FactoryID: testFactory, IssueID: issueOne, State: core.StateBlocked, Version: 3, Attempt: 2, RunID: "run-1", UpdatedAt: now.Add(-3 * time.Hour)},
				{FactoryID: testFactory, IssueID: issueTwo, State: core.StateReady, Version: 1, UpdatedAt: now.Add(-10 * time.Minute)}}}
		}),
		readAction("runs.inspect", func() any {
			return runactions.Inspection{Record: core.StateRecord{FactoryID: testFactory, IssueID: issueOne, State: core.StateBlocked, Version: 3, Attempt: 2, RunID: "run-1", UpdatedAt: now}}
		}),
		readAction("runs.explain", func() any {
			return runactions.Explanation{Issue: issueOne.String(), State: core.StateBlocked, Stopped: true, Cause: runactions.CauseBlocked,
				Reason: f.hostile, Evidence: []runactions.Evidence{{Kind: "state_record", Ref: "issue:" + f.hostile, Summary: f.hostile, At: now}},
				NextActions: []core.NextAction{{Action: "approvals.list", Description: "see the open approvals"},
					{Action: "runs.inspect", Description: "inspect the run", Params: map[string]string{"issue": issueOne.String()}}}}
		}),
		readAction("budget.get", func() any {
			return budget.Status{Confirmed: f.budgetConfirmed, Mode: core.BudgetUnlimited, Settings: budget.Settings{Mode: core.BudgetUnlimited},
				Warnings: []string{"no hard cap: " + f.hostile}}
		}),
		readAction("features.list", func() any {
			return map[string]any{"features": []map[string]any{{"name": f.hostile, "status": "live", "owners": []string{"a", "b"}}}}
		}),
		readAction("secrets.list_refs", func() any {
			refs := []map[string]string{}
			if f.githubRef {
				refs = append(refs, map[string]string{"provider": "github", "name": "app-private-key"})
			}
			return map[string]any{"refs": refs}
		}),
		{Spec: core.ActionSpec{Name: "factory.emergency_stop", Summary: "Stop the factory.", Sensitivity: core.SensitivitySensitive,
			Scopes: []string{"test:admin"}, Roles: ownerOnly, StepUp: true}, Handle: s.recorder(f.hostile, "factory/stop")},
	}
	if f.sample {
		out = append(out, core.Action{Spec: core.ActionSpec{Name: "setup.sample_issue", Summary: "Run a sample issue.", Sensitivity: core.SensitivityWrite,
			Scopes: []string{"test:write"}, Roles: ownerOnly}, Handle: s.recorder(f.hostile, "issue/sample")})
	}
	return out
}

// recorder is a write handler: a dry run reports its change, a real call is
// recorded and answers done.
func (s *seen) recorder(resource, plan string) core.ActionHandler {
	return func(_ context.Context, req core.ActionRequest) (core.ActionResult, error) {
		s.add(req)
		if req.DryRun {
			return core.ActionResult{Plan: &core.DryRun{Changes: []core.Change{{Resource: plan + ":" + resource, Op: core.ChangeUpdate}}}}, nil
		}
		return actions.JSONResult(map[string]bool{"done": true})
	}
}

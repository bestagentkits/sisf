package actions_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestDryRunReportsTheChangeAndChangesNothing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		action           string
		requiresApproval bool
	}{
		{"widgets.rename", false},
		{"widgets.purge", true},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			res, err := f.svc.Invoke(context.Background(), tc.action, core.ActionRequest{
				Actor: f.maintainer(), Input: input(`{"id":"w1","name":"beta"}`), DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			if res.Plan == nil || len(res.Plan.Changes) != 1 || res.Plan.Changes[0].Resource != "widget:w1" {
				t.Fatalf("plan = %+v", res.Plan)
			}
			if res.Plan.RequiresApproval != tc.requiresApproval {
				t.Fatalf("requires_approval = %v, want %v", res.Plan.RequiresApproval, tc.requiresApproval)
			}
			if names, applied := f.widgets.snapshot(); names["w1"] != "alpha" || applied != 0 {
				t.Fatalf("a dry run changed the widgets: %v, %d", names, applied)
			}
			if list, _ := f.approvals.List(context.Background(), approvals.Filter{}); len(list) != 0 {
				t.Fatalf("a dry run filed %d approval requests", len(list))
			}
			if events := f.audit.Events(); len(events) != 0 {
				t.Fatalf("a dry run wrote %d audit records", len(events))
			}
		})
	}
}

func TestDryRunStillNeedsAuthorization(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.Invoke(context.Background(), "widgets.purge", core.ActionRequest{
		Actor: viewer(), Input: input(`{"id":"w1"}`), DryRun: true})
	requireCode(t, err, core.CodePermissionDenied)
}

func TestDryRunWithoutAPlanIsRefused(t *testing.T) {
	t.Parallel()
	lazy := core.Action{Spec: core.ActionSpec{Name: "widgets.touch", Summary: "Touch.", Sensitivity: core.SensitivityWrite,
		Scopes: []string{"widgets:write"}, Roles: []core.Role{core.RoleMaintainer}},
		Handle: func(context.Context, core.ActionRequest) (core.ActionResult, error) { return core.ActionResult{}, nil }}
	f := newFixture(t, lazy)
	_, err := f.svc.Invoke(context.Background(), "widgets.touch", core.ActionRequest{Actor: f.maintainer(), DryRun: true})
	requireCode(t, err, core.CodeInternal)
}

package actions_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

const purgeW1 = `{"id":"w1"}`

func (f fixture) execute(actor core.Actor, name, in, approvalID string) (core.ActionResult, error) {
	return f.svc.Invoke(context.Background(), name, core.ActionRequest{Actor: actor, Input: input(in), ApprovalID: approvalID})
}

func (f fixture) applied() int {
	_, n := f.widgets.snapshot()
	return n
}

func TestSensitiveWriteGoesThroughPlanApprovalExecuteAudit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.plan(t, f.maintainer(), "widgets.purge", purgeW1)
	request, err := f.approvals.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if request.Action != "widgets.purge" || !request.StepUp || request.PolicyRevision != policyA ||
		request.RiskSummary != "Removes widget w1 for good." || request.Diff == "" {
		t.Fatalf("filed request = %+v", request)
	}
	if f.applied() != 0 {
		t.Fatal("planning must not act")
	}
	if again := f.plan(t, f.maintainer(), "widgets.purge", purgeW1); again != id {
		t.Fatalf("planning the same change again filed %s instead of returning %s", again, id)
	}

	f.approve(t, id, approvals.DecisionApprove)
	res, err := f.execute(f.maintainer(), "widgets.purge", purgeW1, id)
	if err != nil {
		t.Fatalf("approved execution failed: %v", err)
	}
	if names, applied := f.widgets.snapshot(); applied != 1 || names["w1"] != "" {
		t.Fatalf("widgets = %v after %d changes", names, applied)
	}
	executed := f.audit.Find("widgets.purge", core.OutcomeExecuted)
	if len(executed) != 1 || res.AuditID == "" || executed[0].Details["approval_id"] != id ||
		executed[0].SubjectDigest != request.SubjectDigest || executed[0].PolicySHA != policyA {
		t.Fatalf("execution audit = %+v, audit id %q", executed, res.AuditID)
	}
	phases := map[string]int{}
	for _, e := range f.audit.Find("widgets.purge", core.OutcomeAllowed) {
		phases[e.Details["phase"]]++
	}
	if phases["plan"] != 2 || phases["execute"] != 1 {
		t.Fatalf("announced phases = %v, want both plans and the execution", phases)
	}
}

func TestApprovalPipelineRefusals(t *testing.T) {
	t.Parallel()
	type step func(t *testing.T, f fixture) (core.Actor, string, string) // actor, input, approval id
	cases := map[string]struct {
		prepare step
		action  string
		want    core.ErrorCode
	}{
		"missing approval: request still pending": {func(t *testing.T, f fixture) (core.Actor, string, string) {
			return f.maintainer(), purgeW1, f.plan(t, f.maintainer(), "widgets.purge", purgeW1)
		}, "widgets.purge", core.CodeApprovalRequired},
		"unknown approval id": {func(t *testing.T, f fixture) (core.Actor, string, string) {
			return f.maintainer(), purgeW1, "no-such-request"
		}, "widgets.purge", core.CodeNotFound},
		"rejected approval": {func(t *testing.T, f fixture) (core.Actor, string, string) {
			id := f.plan(t, f.maintainer(), "widgets.purge", purgeW1)
			f.approve(t, id, approvals.DecisionReject)
			return f.maintainer(), purgeW1, id
		}, "widgets.purge", core.CodePermissionDenied},
		"changed parameters": {func(t *testing.T, f fixture) (core.Actor, string, string) {
			id := f.plan(t, f.maintainer(), "widgets.purge", purgeW1)
			f.approve(t, id, approvals.DecisionApprove)
			return f.maintainer(), `{"id":"w2"}`, id
		}, "widgets.purge", core.CodeApprovalRequired},
		"approval of another action": {func(t *testing.T, f fixture) (core.Actor, string, string) {
			id := f.plan(t, f.owner(), "keys.rotate", purgeW1)
			f.approve(t, id, approvals.DecisionApprove)
			return f.owner(), purgeW1, id
		}, "widgets.purge", core.CodeApprovalRequired},
		"policy changed since the approval": {func(t *testing.T, f fixture) (core.Actor, string, string) {
			id := f.plan(t, f.maintainer(), "widgets.purge", purgeW1)
			f.approve(t, id, approvals.DecisionApprove)
			f.policy.set(policyB)
			return f.maintainer(), purgeW1, id
		}, "widgets.purge", core.CodeApprovalRequired},
		"missing step-up": {func(t *testing.T, f fixture) (core.Actor, string, string) {
			id := f.plan(t, f.maintainer(), "widgets.purge", purgeW1)
			f.approve(t, id, approvals.DecisionApprove)
			actor := f.maintainer()
			actor.StepUpAt = time.Time{}
			return actor, purgeW1, id
		}, "widgets.purge", core.CodeStepUpRequired},
		"stale step-up": {func(t *testing.T, f fixture) (core.Actor, string, string) {
			id := f.plan(t, f.maintainer(), "widgets.purge", purgeW1)
			f.approve(t, id, approvals.DecisionApprove)
			actor := f.maintainer()
			f.clock.Advance(time.Hour)
			return actor, purgeW1, id
		}, "widgets.purge", core.CodeStepUpRequired},
		"agent with an ambient Owner session on an Owner-only action": {func(t *testing.T, f fixture) (core.Actor, string, string) {
			id := f.plan(t, f.owner(), "keys.rotate", purgeW1)
			f.approve(t, id, approvals.DecisionApprove)
			agent := f.owner()
			agent.ID, agent.Kind, agent.Surface = "agent-1", core.ActorAgent, core.SurfaceWebMCP
			return agent, purgeW1, id
		}, "keys.rotate", core.CodePermissionDenied},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			actor, in, id := tc.prepare(t, f)
			_, err := f.execute(actor, tc.action, in, id)
			requireCode(t, err, tc.want)
			if f.applied() != 0 {
				t.Fatal("a refused execution must not act")
			}
		})
	}
}

func TestReusedApprovalIsReplayed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.authz.set("widgets.rename", core.EffectRequireApproval)
	in := `{"id":"w1","name":"beta"}`
	id := f.plan(t, f.maintainer(), "widgets.rename", in)
	f.approve(t, id, approvals.DecisionApprove)
	if _, err := f.execute(f.maintainer(), "widgets.rename", in, id); err != nil {
		t.Fatal(err)
	}
	_, err := f.execute(f.maintainer(), "widgets.rename", in, id)
	requireCode(t, err, core.CodeReplayed)
	if f.applied() != 1 {
		t.Fatalf("the change ran %d times, want once", f.applied())
	}
	if next := f.plan(t, f.maintainer(), "widgets.rename", in); next == id {
		t.Fatal("planning after a used approval must file a new request")
	}
}

func TestChangedParametersLeaveTheApprovalUnspent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.plan(t, f.maintainer(), "widgets.purge", purgeW1)
	f.approve(t, id, approvals.DecisionApprove)
	if _, err := f.execute(f.maintainer(), "widgets.purge", `{"id":"w2"}`, id); !core.HasCode(err, core.CodeApprovalRequired) {
		t.Fatalf("changed parameters = %v", err)
	}
	if _, err := f.execute(f.maintainer(), "widgets.purge", ` { "id" : "w1" } `, id); err != nil {
		t.Fatalf("the approved parameters, formatted differently, must still run: %v", err)
	}
}

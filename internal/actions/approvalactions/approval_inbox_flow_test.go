package approvalactions_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/actions/approvalactions"
	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

const policy core.CommitSHA = "3333333333333333333333333333333333333333"

type allowByRole struct{}

func (allowByRole) Authorize(_ context.Context, req core.AuthzRequest) (core.AuthzDecision, error) {
	for _, role := range req.Action.Roles {
		if req.Actor.HasRole(role) {
			return core.AuthzDecision{Effect: core.EffectAllow}, nil
		}
	}
	return core.AuthzDecision{Effect: core.EffectDeny}, nil
}

// confirmations stands for the step-ups a person makes in the dashboard,
// each for one call. With auto set every call is confirmed as it is made.
type confirmations struct {
	mu    sync.Mutex
	clock *intaketest.Clock
	auto  bool
	made  map[[3]string]int
}

func (c *confirmations) ConsumeStepUp(_ context.Context, session, action, target string) (time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := [3]string{session, action, target}
	if !c.auto {
		if c.made[key] == 0 {
			return time.Time{}, core.NewError(core.CodeStepUpRequired, "no confirmation of this call")
		}
		c.made[key]--
	}
	return c.clock.Now(), nil
}

func (c *confirmations) confirm(actor core.Actor, action, target string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.made[[3]string{actor.SessionID, action, target}]++
}

type flow struct {
	svc     *actions.Service
	clock   *intaketest.Clock
	store   *approvals.KVStore
	applied *int
	stepUps *confirmations
}

func newFlow(t *testing.T) flow {
	t.Helper()
	clock := intaketest.NewClock(time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC))
	keys := intaketest.NewKeys("k")
	store, err := approvals.NewStore(approvals.Config{State: intaketest.NewState(clock), FactoryID: "acme-app",
		Signer: keys, Verifier: keys, Audit: &intaketest.AuditLog{}, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := approvalactions.New(store)
	if err != nil {
		t.Fatal(err)
	}
	applied := 0
	reset := core.Action{Spec: core.ActionSpec{Name: "keys.reset", Summary: "Reset keys.", Sensitivity: core.SensitivitySensitive,
		Scopes: []string{"keys:admin"}, Roles: []core.Role{core.RoleOwner}, StepUp: true},
		Handle: func(_ context.Context, req core.ActionRequest) (core.ActionResult, error) {
			if req.DryRun {
				return core.ActionResult{Plan: &core.DryRun{Changes: []core.Change{{Resource: "keys", Op: core.ChangeUpdate}}}}, nil
			}
			applied++
			return actions.JSONResult(map[string]bool{"reset": true})
		}}
	reg := actions.NewRegistry()
	if err := reg.Register(append(inbox, reset)...); err != nil {
		t.Fatal(err)
	}
	stepUps := &confirmations{clock: clock, made: map[[3]string]int{}}
	svc, err := actions.NewService(actions.Config{FactoryID: "acme-app", Registry: reg, Authorizer: allowByRole{},
		Approvals: store, Audit: &intaketest.AuditLog{}, Clock: clock, StepUp: stepUps,
		Policy: actions.PolicyRevisionFunc(func(context.Context) (core.CommitSHA, error) { return policy, nil })})
	if err != nil {
		t.Fatal(err)
	}
	return flow{svc: svc, clock: clock, store: store, applied: &applied, stepUps: stepUps}
}

func (f flow) owner() core.Actor {
	return core.Actor{ID: "u-owner", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceDashboard,
		SessionID: "ses_owner"}
}

func (f flow) call(t *testing.T, actor core.Actor, name string, in any, dry bool, approvalID string) (core.ActionResult, error) {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return f.svc.Invoke(context.Background(), name, core.ActionRequest{Actor: actor, Input: raw, DryRun: dry, ApprovalID: approvalID})
}

func TestInboxListsGetsAndDecidesThroughTheActionService(t *testing.T) {
	t.Parallel()
	f := newFlow(t)
	_, err := f.call(t, f.owner(), "keys.reset", map[string]any{}, false, "")
	var refusal *core.Error
	if !errors.As(err, &refusal) || refusal.Code != core.CodeStepUpRequired || refusal.Details["step_up_action"] != "keys.reset" {
		t.Fatalf("an unconfirmed plan = %v", err)
	}
	f.stepUps.confirm(f.owner(), "keys.reset", refusal.Details["step_up_target"])
	_, err = f.call(t, f.owner(), "keys.reset", map[string]any{}, false, "")
	if !core.HasCode(err, core.CodeApprovalRequired) {
		t.Fatalf("plan = %v", err)
	}

	res, err := f.call(t, f.owner(), approvalactions.ActionList, map[string]any{"statuses": []string{"pending"}}, false, "")
	var list approvalactions.RequestList
	if err != nil || json.Unmarshal(res.Output, &list) != nil || len(list.Requests) != 1 {
		t.Fatalf("list = %s, %v", res.Output, err)
	}
	request := list.Requests[0]
	res, err = f.call(t, f.owner(), approvalactions.ActionGet, map[string]string{"id": request.ID}, false, "")
	var proposal approvalactions.Proposal
	if err != nil || json.Unmarshal(res.Output, &proposal) != nil || proposal.Decision != nil || proposal.Request.Diff == "" {
		t.Fatalf("get = %s, %v", res.Output, err)
	}

	decision := map[string]any{"request_id": request.ID, "nonce": request.Nonce, "subject_digest": request.SubjectDigest,
		"policy_revision": request.PolicyRevision, "decision": "approve", "reason": "fine"}
	res, err = f.call(t, f.owner(), approvalactions.ActionDecide, decision, true, "")
	if err != nil || res.Plan == nil || res.Plan.RequiresApproval {
		t.Fatalf("decide dry run = %+v, %v", res, err)
	}
	if r, _ := f.store.Get(context.Background(), request.ID); r.Status != approvals.StatusPending {
		t.Fatal("a dry run decision must change nothing")
	}

	agent := f.owner()
	agent.ID, agent.Kind = "agent-1", core.ActorAgent
	if _, err := f.call(t, agent, approvalactions.ActionDecide, decision, false, ""); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("an agent deciding = %v, want PERMISSION_DENIED", err)
	}
	presented := f.owner()
	presented.StepUpAt = f.clock.Now()
	if _, err := f.call(t, presented, approvalactions.ActionDecide, decision, false, ""); !core.HasCode(err, core.CodeStepUpRequired) {
		t.Fatalf("deciding without a confirmation = %v, want STEP_UP_REQUIRED", err)
	}
	// A confirmation names the verdict as well as the request: one made to
	// reject cannot approve.
	f.stepUps.confirm(f.owner(), approvalactions.ActionDecide, actions.DecisionStepUpTarget(request.ID, approvals.DecisionReject))
	_, err = f.call(t, f.owner(), approvalactions.ActionDecide, decision, false, "")
	if !errors.As(err, &refusal) || refusal.Code != core.CodeStepUpRequired ||
		refusal.Details[actions.DetailStepUpTarget] != actions.DecisionStepUpTarget(request.ID, approvals.DecisionApprove) {
		t.Fatalf("approving on a confirmation to reject = %v", err)
	}
	f.stepUps.confirm(f.owner(), approvalactions.ActionDecide, actions.DecisionStepUpTarget(request.ID, approvals.DecisionApprove))
	if _, err := f.call(t, f.owner(), approvalactions.ActionDecide, decision, false, ""); err != nil {
		t.Fatalf("decide = %v", err)
	}
	if _, err := f.call(t, f.owner(), approvalactions.ActionDecide, decision, false, ""); !core.HasCode(err, core.CodeStepUpRequired) {
		t.Fatalf("deciding twice on one confirmation = %v, want STEP_UP_REQUIRED", err)
	}
	f.stepUps.confirm(f.owner(), approvalactions.ActionDecide, actions.DecisionStepUpTarget(request.ID, approvals.DecisionApprove))
	if _, err := f.call(t, f.owner(), approvalactions.ActionDecide, decision, false, ""); !core.HasCode(err, core.CodeReplayed) {
		t.Fatalf("deciding twice = %v, want REPLAYED", err)
	}

	if _, err := f.call(t, f.owner(), "keys.reset", map[string]any{}, false, request.ID); !core.HasCode(err, core.CodeStepUpRequired) {
		t.Fatalf("an unconfirmed execution = %v", err)
	}
	f.stepUps.confirm(f.owner(), "keys.reset", request.ID)
	if _, err := f.call(t, f.owner(), "keys.reset", map[string]any{}, false, request.ID); err != nil || *f.applied != 1 {
		t.Fatalf("approved execution = %v, applied %d", err, *f.applied)
	}
}

func TestInboxRefusesBadInput(t *testing.T) {
	t.Parallel()
	f := newFlow(t)
	f.stepUps.auto = true
	cases := []struct {
		action string
		in     any
		want   core.ErrorCode
	}{
		{approvalactions.ActionList, map[string]any{"statuses": []string{"maybe"}}, core.CodeInvalidArgument},
		{approvalactions.ActionList, map[string]any{"limit": 900}, core.CodeInvalidArgument},
		{approvalactions.ActionList, map[string]any{"issue": "nope"}, core.CodeInvalidArgument},
		{approvalactions.ActionGet, map[string]any{}, core.CodeInvalidArgument},
		{approvalactions.ActionGet, map[string]any{"id": "missing"}, core.CodeNotFound},
		{approvalactions.ActionDecide, map[string]any{"request_id": "missing", "decision": "approve"}, core.CodeNotFound},
		{approvalactions.ActionDecide, map[string]any{"request_id": "x", "decision": "maybe"}, core.CodeInvalidArgument},
	}
	for _, tc := range cases {
		if _, err := f.call(t, f.owner(), tc.action, tc.in, false, ""); !core.HasCode(err, tc.want) {
			t.Errorf("%s %v: error = %v, want %s", tc.action, tc.in, err, tc.want)
		}
	}
	if _, err := approvalactions.New(nil); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("New(nil) = %v", err)
	}
}

package budget_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

const policySHA core.CommitSHA = "1111111111111111111111111111111111111111"

// roleAuthorizer allows an actor that holds one of the action's roles.
type roleAuthorizer struct{}

func (roleAuthorizer) Authorize(_ context.Context, req core.AuthzRequest) (core.AuthzDecision, error) {
	if slices.ContainsFunc(req.Action.Roles, req.Actor.HasRole) {
		return core.AuthzDecision{Effect: core.EffectAllow}, nil
	}
	return core.AuthzDecision{Effect: core.EffectDeny, Reason: "role"}, nil
}

// confirmedSessions stands for the step-ups people make in the dashboard:
// every call made in a session ending in confirmedSuffix is confirmed as
// it is made, and no other call is.
type confirmedSessions struct{ clock interface{ Now() time.Time } }

const confirmedSuffix = "-confirmed"

func (c confirmedSessions) ConsumeStepUp(_ context.Context, session, _, _ string) (time.Time, error) {
	if !strings.HasSuffix(session, confirmedSuffix) {
		return time.Time{}, core.NewError(core.CodeStepUpRequired, "no confirmation of this call")
	}
	return c.clock.Now(), nil
}

type pipeline struct {
	svc       *actions.Service
	approvals *approvals.KVStore
	env       *env
}

func newPipeline(t *testing.T) pipeline {
	t.Helper()
	e := newEnv(t)
	e.confirmUnlimited(0)
	state, keys, audit := intaketest.NewState(e.clock), intaketest.NewKeys("approval-key"), &intaketest.AuditLog{}
	store, err := approvals.NewStore(approvals.Config{State: state, FactoryID: factory, Signer: keys, Verifier: keys, Audit: audit, Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	acts, err := budget.Actions(budget.ActionsConfig{Store: e.ledger, Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	reg := actions.NewRegistry()
	if err := reg.Register(acts...); err != nil {
		t.Fatal(err)
	}
	svc, err := actions.NewService(actions.Config{FactoryID: factory, Registry: reg, Authorizer: roleAuthorizer{}, Approvals: store,
		Policy: actions.PolicyRevisionFunc(func(context.Context) (core.CommitSHA, error) { return policySHA, nil }),
		Audit:  audit, Clock: e.clock, StepUpMaxAge: 5 * time.Minute, StepUp: confirmedSessions{e.clock}})
	if err != nil {
		t.Fatal(err)
	}
	return pipeline{svc: svc, approvals: store, env: e}
}

// actor is a person in a dashboard session. With stepUp set the session
// confirms each call, and StepUpAt is set for the approval store, which the
// tests also call directly.
func (p pipeline) actor(role core.Role, stepUp bool) core.Actor {
	a := core.Actor{ID: "u-" + string(role), Kind: core.ActorHuman, Roles: []core.Role{role}, Surface: core.SurfaceDashboard,
		SessionID: "ses-" + string(role)}
	if stepUp {
		a.SessionID += confirmedSuffix
		a.StepUpAt = p.env.clock.Now()
	}
	return a
}

func (p pipeline) invoke(a core.Actor, in, approvalID string) (core.ActionResult, error) {
	return p.svc.Invoke(context.Background(), budget.ActionSet, core.ActionRequest{Actor: a, Input: json.RawMessage(in), ApprovalID: approvalID})
}

func (p pipeline) unchanged(t *testing.T) {
	t.Helper()
	if _, err := p.env.ledger.GetKV(context.Background(), budget.SettingsNamespace, budget.SettingsKey); !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("the settings were written without approval: %v", err)
	}
}

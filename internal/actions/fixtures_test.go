package actions_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	testFactory core.FactoryID = "acme-app"
	policyA     core.CommitSHA = "1111111111111111111111111111111111111111"
	policyB     core.CommitSHA = "2222222222222222222222222222222222222222"
)

var start = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

// fakeAuthorizer allows an actor that holds one of the action's roles and,
// when it carries scopes, every scope the action names. Effects overrides
// the answer per action; err makes every decision fail.
type fakeAuthorizer struct {
	mu      sync.Mutex
	effects map[string]core.AuthzEffect
	err     error
	calls   int
}

func (a *fakeAuthorizer) Authorize(_ context.Context, req core.AuthzRequest) (core.AuthzDecision, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.err != nil {
		return core.AuthzDecision{}, a.err
	}
	if effect, ok := a.effects[req.Action.Name]; ok {
		return core.AuthzDecision{Effect: effect, Reason: "overridden"}, nil
	}
	if !slices.ContainsFunc(req.Action.Roles, req.Actor.HasRole) {
		return core.AuthzDecision{Effect: core.EffectDeny, Reason: "role"}, nil
	}
	for _, scope := range req.Action.Scopes {
		if len(req.Actor.Scopes) > 0 && !slices.Contains(req.Actor.Scopes, scope) {
			return core.AuthzDecision{Effect: core.EffectDeny, Reason: "scope"}, nil
		}
	}
	return core.AuthzDecision{Effect: core.EffectAllow}, nil
}

func (a *fakeAuthorizer) set(name string, effect core.AuthzEffect) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.effects == nil {
		a.effects = map[string]core.AuthzEffect{}
	}
	a.effects[name] = effect
}

// widgets is the in-memory domain the sample actions change, so a test can
// see exactly which side effects happened.
type widgets struct {
	mu      sync.Mutex
	names   map[string]string
	applied int
}

func (w *widgets) snapshot() (map[string]string, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]string{}
	for k, v := range w.names {
		out[k] = v
	}
	return out, w.applied
}

type fixture struct {
	svc       *actions.Service
	authz     *fakeAuthorizer
	approvals *approvals.KVStore
	state     *intaketest.State
	audit     *intaketest.AuditLog
	clock     *intaketest.Clock
	widgets   *widgets
	policy    *policyRevision
}

type policyRevision struct {
	mu  sync.Mutex
	sha core.CommitSHA
}

func (p *policyRevision) PolicyRevision(context.Context) (core.CommitSHA, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sha, nil
}

func (p *policyRevision) set(sha core.CommitSHA) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sha = sha
}

func newFixture(t *testing.T, extra ...core.Action) fixture {
	t.Helper()
	clock := intaketest.NewClock(start)
	f := fixture{
		authz: &fakeAuthorizer{}, state: intaketest.NewState(clock), audit: &intaketest.AuditLog{},
		clock: clock, widgets: &widgets{names: map[string]string{"w1": "alpha"}}, policy: &policyRevision{sha: policyA},
	}
	keys := intaketest.NewKeys("approval-key")
	store, err := approvals.NewStore(approvals.Config{State: f.state, FactoryID: testFactory, Signer: keys,
		Verifier: keys, Audit: f.audit, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	f.approvals = store
	reg := actions.NewRegistry()
	if err := reg.Register(append(sampleActions(f.widgets), extra...)...); err != nil {
		t.Fatal(err)
	}
	f.svc, err = actions.NewService(actions.Config{FactoryID: testFactory, Registry: reg, Authorizer: f.authz,
		Approvals: store, Policy: f.policy, Audit: f.audit, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// Actors. Every human here confirmed their identity just now unless a test
// says otherwise.
func (f fixture) owner() core.Actor {
	return core.Actor{ID: "u-owner", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner},
		Surface: core.SurfaceAPI, StepUpAt: f.clock.Now()}
}

func (f fixture) maintainer() core.Actor {
	return core.Actor{ID: "u-maint", Kind: core.ActorHuman, Roles: []core.Role{core.RoleMaintainer},
		Surface: core.SurfaceCLI, StepUpAt: f.clock.Now()}
}

func viewer() core.Actor {
	return core.Actor{ID: "u-viewer", Kind: core.ActorHuman, Roles: []core.Role{core.RoleViewer}, Surface: core.SurfaceDashboard}
}

func input(v string) json.RawMessage { return json.RawMessage(v) }

// approve records the Owner's approval of a filed request, as the approval
// inbox does on any surface.
func (f fixture) approve(t *testing.T, requestID string, decision approvals.Decision) {
	t.Helper()
	r, err := f.approvals.Get(context.Background(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.approvals.Submit(context.Background(), f.owner(), approvals.Submission{RequestID: r.ID, Nonce: r.Nonce,
		SubjectDigest: r.SubjectDigest, PolicyRevision: r.PolicyRevision, Decision: decision, Reason: "checked"})
	if err != nil {
		t.Fatalf("the Owner could not decide: %v", err)
	}
}

// plan invokes a sensitive action without an approval and returns the id of
// the approval request it filed.
func (f fixture) plan(t *testing.T, actor core.Actor, name, in string) string {
	t.Helper()
	_, err := f.svc.Invoke(context.Background(), name, core.ActionRequest{Actor: actor, Input: input(in)})
	typed := asError(t, err, core.CodeApprovalRequired)
	id := typed.Details["approval_id"]
	if id == "" {
		t.Fatalf("the plan names no approval request: %+v", typed)
	}
	return id
}

func asError(t *testing.T, err error, code core.ErrorCode) *core.Error {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("error = %v, want %s", err, code)
	}
	var typed *core.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %T is not a *core.Error", err)
	}
	if len(typed.NextActions) == 0 {
		t.Fatalf("error %v carries no next_actions", err)
	}
	return typed
}

// requireCode fails the test unless err is a typed error of code that
// carries next_actions.
func requireCode(t *testing.T, err error, code core.ErrorCode) {
	t.Helper()
	_ = asError(t, err, code)
}

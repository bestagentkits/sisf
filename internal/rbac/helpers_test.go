package rbac_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/rbac"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

var start = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

// installs is the factory's installations, by "owner/repo".
type installs map[string]identity.Installation

func (m installs) Installation(_ context.Context, owner, repo string) (identity.Installation, error) {
	if inst, ok := m[owner+"/"+repo]; ok {
		return inst, nil
	}
	return identity.Installation{}, core.NewError(core.CodeNotFound, "not installed")
}

// live is the set of live session and token handles.
type live struct {
	mu      sync.Mutex
	handles map[string]bool
}

func (l *live) Active(_ context.Context, h string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.handles[h]
}

func (l *live) end(h string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.handles, h)
}

// logins maps actor IDs to GitHub logins.
type logins map[string]string

func (m logins) GitHubLogin(_ context.Context, actorID string) (string, bool, error) {
	l, ok := m[actorID]
	return l, ok, nil
}

// perms is a membership source: "owner/repo|login" to permission.
type perms struct {
	mu sync.Mutex
	m  map[string]string
}

func (p *perms) RepoPermission(_ context.Context, owner, repo, login string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.m[owner+"/"+repo+"|"+login]; ok {
		return v, nil
	}
	return "none", nil
}

func (p *perms) set(key, v string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[key] = v
}

type fixture struct {
	authz  *rbac.Authorizer
	grants *rbac.Grants
	live   *live
	perms  *perms
	clock  *sqlstoretest.Clock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{grants: rbac.NewGrants(), clock: sqlstoretest.NewClock(start),
		live:  &live{handles: map[string]bool{"ses_alice": true, "ses_owner": true, "tok_bot": true, "tok_alice": true}},
		perms: &perms{m: map[string]string{"acme/app|alice": "maintain", "acme/other|alice": "admin"}}}
	authz, err := rbac.New(rbac.Config{
		Grants: f.grants, Sessions: f.live, Clock: f.clock, DefaultRepo: "acme/app",
		Installations: installs{"acme/app": {ID: 7, Owner: "acme", Repo: "app"}, "acme/paused": {ID: 8, Owner: "acme", Repo: "paused", Suspended: true}},
		Membership:    f.perms, Accounts: logins{"user:alice": "alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.authz = authz
	if err := f.grants.Add("user:owner", core.ActorHuman, core.RoleOwner); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) decide(t *testing.T, actor core.Actor, spec core.ActionSpec, resource string) core.AuthzDecision {
	t.Helper()
	d, err := f.authz.Authorize(context.Background(), core.AuthzRequest{Actor: actor, Action: spec, Resource: resource})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	return d
}

var (
	alice     = core.Actor{ID: "user:alice", Kind: core.ActorHuman, SessionID: "ses_alice"}
	owner     = core.Actor{ID: "user:owner", Kind: core.ActorHuman, SessionID: "ses_owner"}
	bot       = core.Actor{ID: "agent:coder", Kind: core.ActorAgent, SessionID: "tok_bot", Scopes: []string{"runs:write"}}
	mergeSpec = core.ActionSpec{Name: "prs.merge", Sensitivity: core.SensitivityWrite, Roles: []core.Role{core.RoleMaintainer, core.RoleOwner}, Scopes: []string{"prs:write"}}
	policy    = core.ActionSpec{Name: "policy.update", Sensitivity: core.SensitivitySensitive, Roles: []core.Role{core.RoleOwner}, StepUp: true, Scopes: []string{"policy:write"}}
	runSpec   = core.ActionSpec{Name: "runs.start", Sensitivity: core.SensitivityWrite, Roles: []core.Role{core.RoleAgent}, Scopes: []string{"runs:write"}}
)

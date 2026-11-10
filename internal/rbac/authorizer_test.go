package rbac_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/rbac"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestRolesComeFromGitHubPermissionAndLocalGrants(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if d := f.decide(t, alice, mergeSpec, ""); !d.Allowed() {
		t.Fatalf("a GitHub maintainer was refused: %+v", d)
	}
	roles, err := f.authz.Roles(context.Background(), owner, "acme", "app")
	if err != nil || len(roles) != 1 || roles[0] != core.RoleOwner {
		t.Fatalf("owner roles = %v %v", roles, err)
	}
	if d := f.decide(t, alice, policy, ""); d.Effect != core.EffectDeny {
		t.Fatalf("a GitHub admin or maintainer must never be an Owner: %+v", d)
	}
}

func TestClaimedRolesAndLabelsNeverConferARole(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// An actor whose roles were filled in from issue labels such as
	// "role:owner" or "sisf:maintainer", or from any request content.
	claimed := core.Actor{ID: "user:mallory", Kind: core.ActorHuman, SessionID: "ses_alice",
		Roles: []core.Role{core.RoleOwner, core.RoleMaintainer}, StepUpAt: f.clock.Now()}
	for _, spec := range []core.ActionSpec{mergeSpec, policy} {
		if d := f.decide(t, claimed, spec, "acme/app#12"); d.Effect != core.EffectDeny {
			t.Fatalf("%s: claimed roles were trusted: %+v", spec.Name, d)
		}
	}
}

func TestRolesAreResolvedAtActionTime(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if d := f.decide(t, alice, mergeSpec, ""); !d.Allowed() {
		t.Fatalf("setup: %+v", d)
	}
	f.perms.set("acme/app|alice", "read")
	if d := f.decide(t, alice, mergeSpec, ""); d.Effect != core.EffectDeny {
		t.Fatalf("a GitHub demotion did not apply at the next action: %+v", d)
	}
	f.grants.Remove("user:owner", core.RoleOwner)
	if d := f.decide(t, owner, mergeSpec, ""); d.Effect != core.EffectDeny {
		t.Fatalf("a withdrawn grant still applied: %+v", d)
	}
}

func TestInstallationMappingIsSeparateFromLogin(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// alice is a GitHub admin of acme/other, but the factory is not
	// installed there: her login gives the factory no reach into it.
	for _, resource := range []string{"acme/other#3", "repo:acme/other", "acme/paused#1", "evil/app#1"} {
		if d := f.decide(t, alice, mergeSpec, resource); d.Effect != core.EffectDeny {
			t.Fatalf("%s: allowed outside the installation: %+v", resource, d)
		}
		if d := f.decide(t, owner, mergeSpec, resource); d.Effect != core.EffectDeny {
			t.Fatalf("%s: even the Owner acts only where the factory is installed: %+v", resource, d)
		}
	}
	if d := f.decide(t, alice, mergeSpec, "repo:acme/app"); !d.Allowed() {
		t.Fatalf("installed repository refused: %+v", d)
	}
	if d := f.decide(t, alice, mergeSpec, "repo:acme"); d.Effect != core.EffectDeny {
		t.Fatalf("a malformed repository resource was accepted: %+v", d)
	}
}

func TestTokensRunOnlyActionsWithinTheirScopes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := f.grants.Add(bot.ID, core.ActorAgent, core.RoleAgent); err != nil {
		t.Fatal(err)
	}
	if d := f.decide(t, bot, runSpec, ""); !d.Allowed() {
		t.Fatalf("an in-scope token was refused: %+v", d)
	}
	narrow := core.Actor{ID: "user:alice", Kind: core.ActorHuman, SessionID: "tok_alice", Scopes: []string{"issues:read"}}
	if d := f.decide(t, narrow, mergeSpec, ""); d.Effect != core.EffectDeny {
		t.Fatalf("a token without prs:write merged: %+v", d)
	}
	unscoped := mergeSpec
	unscoped.Scopes = nil
	if d := f.decide(t, narrow, unscoped, ""); d.Effect != core.EffectDeny {
		t.Fatalf("a token ran an action that has no scope: %+v", d)
	}
}

func TestStepUpIsRequiredFreshAndOnlyFromAPerson(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if d := f.decide(t, owner, policy, ""); d.Effect != core.EffectRequireStepUp {
		t.Fatalf("an ambient session satisfied step-up: %+v", d)
	}
	stepped := owner
	stepped.StepUpAt = f.clock.Now()
	if d := f.decide(t, stepped, policy, ""); !d.Allowed() {
		t.Fatalf("a fresh step-up was refused: %+v", d)
	}
	f.clock.Advance(5*time.Minute + time.Second)
	if d := f.decide(t, stepped, policy, ""); d.Effect != core.EffectRequireStepUp {
		t.Fatalf("a stale step-up was accepted: %+v", d)
	}
	future := owner
	future.StepUpAt = f.clock.Now().Add(time.Hour)
	if d := f.decide(t, future, policy, ""); d.Effect != core.EffectRequireStepUp {
		t.Fatalf("a step-up dated in the future was accepted: %+v", d)
	}
	if err := f.grants.Add("agent:coder", core.ActorAgent, core.RoleAgent); err != nil {
		t.Fatal(err)
	}
	agentPolicy := policy
	agentPolicy.Roles = []core.Role{core.RoleAgent}
	agent := bot
	agent.Scopes, agent.StepUpAt = []string{"policy:write"}, f.clock.Now()
	if d := f.decide(t, agent, agentPolicy, ""); d.Effect != core.EffectDeny {
		t.Fatalf("an agent passed a step-up: %+v", d)
	}
}

func TestRevokedSessionIsDeniedAtActionTime(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.live.end("ses_alice")
	if d := f.decide(t, alice, mergeSpec, ""); d.Effect != core.EffectDeny {
		t.Fatalf("a revoked session acted: %+v", d)
	}
	if d := f.decide(t, core.Actor{ID: "user:alice", Kind: core.ActorHuman}, mergeSpec, ""); d.Effect != core.EffectDeny {
		t.Fatalf("an actor without a session acted: %+v", d)
	}
}

func TestInvalidRequestsAreDenied(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if d, err := f.authz.Authorize(context.Background(), core.AuthzRequest{Actor: owner, Action: core.ActionSpec{Name: "bad"}}); err == nil || d.Allowed() {
		t.Fatalf("an invalid spec was decided: %+v %v", d, err)
	}
	if d := f.decide(t, core.Actor{SessionID: "ses_owner"}, mergeSpec, ""); d.Effect != core.EffectDeny {
		t.Fatalf("an anonymous actor acted: %+v", d)
	}
	for name, cfg := range map[string]rbac.Config{
		"empty":           {},
		"bad repo":        {Grants: rbac.NewGrants(), Installations: installs{}, Sessions: &live{}, Clock: f.clock, DefaultRepo: "acme"},
		"half membership": {Grants: rbac.NewGrants(), Installations: installs{}, Sessions: &live{}, Clock: f.clock, DefaultRepo: "a/b", Membership: &perms{}},
	} {
		if _, err := rbac.New(cfg); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestGrantsKeepPeopleAndMachinesApart(t *testing.T) {
	t.Parallel()
	g := rbac.NewGrants()
	for _, bad := range []struct {
		kind core.ActorKind
		role core.Role
	}{{core.ActorHuman, core.RoleAgent}, {core.ActorHuman, core.RolePublisher}, {core.ActorAgent, core.RoleOwner}, {core.ActorService, core.RoleMaintainer}, {core.ActorHuman, "admin"}} {
		if err := g.Add("x", bad.kind, bad.role); err == nil {
			t.Errorf("granted %s to a %s", bad.role, bad.kind)
		}
	}
	if err := g.Add("svc", core.ActorService, core.RolePublisher); err != nil {
		t.Fatal(err)
	}
	if err := g.Add("svc", core.ActorAgent, core.RoleAgent); err == nil {
		t.Fatal("one actor ID was granted roles under two kinds")
	}
	if roles := g.Roles("svc", core.ActorHuman); len(roles) != 0 {
		t.Fatalf("a service's grants applied to a person with the same ID: %v", roles)
	}
	for perm, want := range map[string]core.Role{"admin": core.RoleMaintainer, "maintain": core.RoleMaintainer, "write": core.RoleReporter, "triage": core.RoleReporter, "read": core.RoleViewer} {
		if got, ok := rbac.RoleForPermission(perm); !ok || got != want {
			t.Errorf("%s -> %s", perm, got)
		}
	}
	if _, ok := rbac.RoleForPermission("none"); ok {
		t.Error("no permission mapped to a role")
	}
}

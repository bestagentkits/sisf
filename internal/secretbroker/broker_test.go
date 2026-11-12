package secretbroker_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/secretbroker"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/secrets"
)

func TestARunRedeemsItsGrantForItsPurposeUntilItExpires(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	g, err := f.broker.Issue(ctx, envGrant("run-0001", modelKey, "ANTHROPIC_API_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	if secretbroker.EnvName(g) != "ANTHROPIC_API_KEY" || !g.ExpiresAt.Equal(start.Add(10*time.Minute)) {
		t.Fatalf("unexpected grant %+v", g)
	}
	use := secretbroker.Use{GrantID: g.ID, RunID: "run-0001", Purpose: secretbroker.PurposeSandboxEnv, Scope: "env:ANTHROPIC_API_KEY"}
	v, err := f.broker.RedeemFor(ctx, use)
	if err != nil || string(v.Reveal()) != "model-key-value-0001" {
		t.Fatalf("redeem: %v", err)
	}

	f.clock.Advance(10 * time.Minute)
	_, err = f.broker.RedeemFor(ctx, use)
	wantCode(t, err, core.CodeExpired, "redeem after the lifetime")
	_, err = f.broker.Grant(ctx, g.ID)
	wantCode(t, err, core.CodeExpired, "describe after the lifetime")
	_, err = f.broker.Redeem(ctx, "grant-unknown")
	wantCode(t, err, core.CodeNotFound, "unknown grant")
	f.assertNoValueLeaked(t, err)
}

func TestLifetimesAreCappedAndPurposesChecked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	req := envGrant("run-0001", modelKey, "ANTHROPIC_API_KEY")
	req.TTL = 24 * time.Hour
	g, err := f.broker.Issue(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !g.ExpiresAt.Equal(start.Add(30 * time.Minute)) {
		t.Fatalf("a day-long grant was not cut to the cap: %s", g.ExpiresAt)
	}
	for name, mutate := range map[string]func(*secrets.GrantRequest){
		"no purpose":            func(r *secrets.GrantRequest) { r.Purpose = "" },
		"purpose with spaces":   func(r *secrets.GrantRequest) { r.Purpose = "anything you like" },
		"no lifetime":           func(r *secrets.GrantRequest) { r.TTL = 0 },
		"env without a scope":   func(r *secrets.GrantRequest) { r.Scopes = nil },
		"env with two names":    func(r *secrets.GrantRequest) { r.Scopes = []string{"env:A", "env:B"} },
		"env lower-case name":   func(r *secrets.GrantRequest) { r.Scopes = []string{"env:path"} },
		"scope with a space":    func(r *secrets.GrantRequest) { r.Scopes = []string{"env:A", "repo write"} },
		"run id not an id":      func(r *secrets.GrantRequest) { r.RunID = "../run" },
		"secret not registered": func(r *secrets.GrantRequest) { r.Ref.Name = "providers/other" },
	} {
		r := envGrant("run-0001", modelKey, "ANTHROPIC_API_KEY")
		mutate(&r)
		if _, err := f.broker.Issue(ctx, r); err == nil {
			t.Errorf("%s: the grant was issued", name)
		}
	}
}

func TestAGrantServesOnlyItsRunPurposeAndScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	g, err := f.broker.Issue(ctx, envGrant("run-0001", modelKey, "ANTHROPIC_API_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	for name, use := range map[string]secretbroker.Use{
		"another run":     {GrantID: g.ID, RunID: "run-0002", Purpose: secretbroker.PurposeSandboxEnv},
		"another purpose": {GrantID: g.ID, RunID: "run-0001", Purpose: "git.push"},
		"another scope":   {GrantID: g.ID, RunID: "run-0001", Purpose: secretbroker.PurposeSandboxEnv, Scope: "env:GITHUB_TOKEN"},
	} {
		_, err := f.broker.RedeemFor(ctx, use)
		wantCode(t, err, core.CodePermissionDenied, name)
	}
}

func TestCancellingARunRevokesEveryGrantAndRefusesNewOnes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	first, err := f.broker.Issue(ctx, envGrant("run-0001", modelKey, "ANTHROPIC_API_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.broker.Issue(ctx, envGrant("run-0002", modelKey, "ANTHROPIC_API_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.broker.RevokeRun(ctx, "run-0001"); err != nil {
		t.Fatal(err)
	}
	_, err = f.broker.Redeem(ctx, first.ID)
	wantCode(t, err, core.CodeExpired, "redeem after cancel")
	_, err = f.broker.Issue(ctx, envGrant("run-0001", modelKey, "ANTHROPIC_API_KEY"))
	wantCode(t, err, core.CodePermissionDenied, "new grant after cancel")
	if _, err := f.broker.Redeem(ctx, other.ID); err != nil {
		t.Fatalf("another run lost its grant: %v", err)
	}
	if len(f.audit.Find("secrets.run.revoke", core.OutcomeExecuted)) != 1 {
		t.Fatal("the revocation was not audited")
	}
	f.assertNoValueLeaked(t)
}

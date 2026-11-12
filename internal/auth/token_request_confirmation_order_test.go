package auth

import (
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
)

// A token request that cannot be honoured is refused before the
// confirmation it names is spent, so a typo in the form does not cost the
// person a fresh sign-in at the provider. A request that is honoured spends
// its confirmation once.
func TestATokenRequestIsCheckedBeforeItsConfirmationIsSpent(t *testing.T) {
	t.Parallel()
	gh, err := NewGitHubProvider(GitHubConfig{ClientID: "c", RedirectURL: "http://127.0.0.1:1/cb"})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{Clock: core.SystemClock{}, Providers: []identity.Provider{gh}, AllowedScopes: []string{"prs:write", "runs:read"}})
	if err != nil {
		t.Fatal(err)
	}
	_, ss, err := svc.createSession(identity.Identity{Issuer: "https://github.com", Subject: "1001", Login: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	confirm := func(req PATRequest) StepUpTarget {
		target := req.StepUpTarget()
		svc.mu.Lock()
		svc.addProofLocked(ss, target, svc.cfg.Clock.Now())
		svc.mu.Unlock()
		return target
	}
	held := func(target StepUpTarget) bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		_, ok := ss.proofs[target]
		return ok
	}

	for name, req := range map[string]PATRequest{
		"no scope":            {Name: "cli"},
		"a malformed scope":   {Name: "cli", Scopes: []string{"Not A Scope"}},
		"a repeated scope":    {Name: "cli", Scopes: []string{"prs:write", "prs:write"}},
		"an unknown scope":    {Name: "cli", Scopes: []string{"policy:write"}},
		"a negative lifetime": {Name: "cli", Scopes: []string{"prs:write"}, TTL: -time.Second},
		"too long a lifetime": {Name: "cli", Scopes: []string{"prs:write"}, TTL: MaxPATTTL + time.Second},
	} {
		target := confirm(req)
		if _, err := svc.IssuePAT(ss.actor(), req); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: %v, want INVALID_ARGUMENT", name, err)
		}
		if !held(target) {
			t.Errorf("%s: the refused request spent its confirmation", name)
		}
	}

	good := PATRequest{Name: "cli", Scopes: []string{"runs:read", "prs:write"}, TTL: time.Hour}
	target := confirm(good)
	if _, err := svc.IssuePAT(ss.actor(), good); err != nil {
		t.Fatalf("the confirmed request: %v", err)
	}
	if held(target) {
		t.Fatal("the honoured request left its confirmation unspent")
	}
}

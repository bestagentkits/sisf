package auth_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/pkg/core"
)

// steppedUp signs subject in with Google, completes a step-up and returns
// the CSRF header for the session.
func steppedUp(t *testing.T, e *env, b *browser, subject string) map[string]string {
	t.Helper()
	b.signIn(t, "google", subject)
	view, _ := b.session(t)
	csrf := map[string]string{auth.CSRFHeader: view.CSRFToken}
	_, providerURL := b.hop(t, http.MethodPost, "/auth/step-up/google", csrf)
	if resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, subject, providerURL), nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("step-up: %d", resp.StatusCode)
	}
	return csrf
}

// mint asks for a personal access token and returns the status and token.
func mint(t *testing.T, b *browser, header map[string]string, body string) (int, string, auth.TokenInfo) {
	t.Helper()
	resp := b.do(t, http.MethodPost, "/auth/tokens", body, header)
	var out struct {
		Token string         `json:"token"`
		Info  auth.TokenInfo `json:"info"`
	}
	if resp.StatusCode == http.StatusCreated {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, out.Token, out.Info
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func TestMintingAPersonalTokenNeedsAFreshStepUp(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	b.signIn(t, "google", "owner-sub")
	view, _ := b.session(t)
	if status, _, _ := mint(t, b, map[string]string{auth.CSRFHeader: view.CSRFToken}, `{"scopes":["prs:write"]}`); status != http.StatusForbidden {
		t.Fatalf("a token was minted without step-up: %d", status)
	}
	if status, _, _ := mint(t, b, nil, `{"scopes":["prs:write"]}`); status != http.StatusForbidden {
		t.Fatalf("a token was minted without a CSRF token: %d", status)
	}
}

func TestPersonalTokensAreLimitedToTheirScopes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	csrf := steppedUp(t, e, b, "owner-sub")
	status, token, info := mint(t, b, csrf, `{"name":"cli","scopes":["prs:write"],"ttl_seconds":3600}`)
	if status != http.StatusCreated || info.Kind != auth.TokenPAT || !info.ExpiresAt.Equal(e.clock.Now().Add(time.Hour)) {
		t.Fatalf("mint: %d %+v", status, info)
	}
	cli := e.browser(t)
	actor, code := cli.actor(t, http.MethodPost, bearer(token))
	if code != http.StatusOK || actor.ID != e.actorID("owner-sub") || actor.SessionID != info.ID || !actor.StepUpAt.IsZero() {
		t.Fatalf("token actor = %+v (%d)", actor, code)
	}
	if d := e.authorize(t, actor, mergeSpec, ""); !d.Allowed() {
		t.Fatalf("an in-scope action was refused: %+v", d)
	}
	outOfScope := policySpec
	outOfScope.StepUp = false
	if d := e.authorize(t, actor, outOfScope, ""); d.Effect != core.EffectDeny {
		t.Fatalf("a token ran an action outside its scopes: %+v", d)
	}
	for name, body := range map[string]string{
		"no scopes": `{"scopes":[]}`, "wildcard": `{"scopes":["*"]}`, "repeated": `{"scopes":["a:b","a:b"]}`,
		"too long-lived": `{"scopes":["prs:write"],"ttl_seconds":691200}`, "negative ttl": `{"scopes":["prs:write"],"ttl_seconds":-1}`,
		"unknown field": `{"scopes":["prs:write"],"admin":true}`,
	} {
		if status, _, _ := mint(t, b, csrf, body); status != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, status)
		}
	}
}

func TestPersonalTokensExpireAndCanBeRevoked(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	csrf := steppedUp(t, e, b, "owner-sub")
	_, short, _ := mint(t, b, csrf, `{"scopes":["prs:write"],"ttl_seconds":60}`)
	_, revoked, info := mint(t, b, csrf, `{"scopes":["prs:write"]}`)
	cli := e.browser(t)
	if _, code := cli.actor(t, http.MethodGet, bearer(revoked)); code != http.StatusOK {
		t.Fatalf("setup: %d", code)
	}
	other := e.browser(t)
	other.signIn(t, "google", "alice-sub")
	otherView, _ := other.session(t)
	if resp := other.do(t, http.MethodDelete, "/auth/tokens/"+info.ID, "", map[string]string{auth.CSRFHeader: otherView.CSRFToken}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("someone else revoked the token: %d", resp.StatusCode)
	}
	if resp := b.do(t, http.MethodDelete, "/auth/tokens/"+info.ID, "", csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if _, code := cli.actor(t, http.MethodGet, bearer(revoked)); code != http.StatusUnauthorized {
		t.Fatalf("a revoked token authenticates: %d", code)
	}
	e.clock.Advance(time.Minute)
	if _, code := cli.actor(t, http.MethodGet, bearer(short)); code != http.StatusUnauthorized {
		t.Fatalf("an expired token authenticates: %d", code)
	}
	if got := e.svc.Tokens(e.actorID("owner-sub")); len(got) != 0 {
		t.Fatalf("ended tokens are still listed: %+v", got)
	}
}

func TestATokenCanNeitherMintTokensNorSatisfyStepUp(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	csrf := steppedUp(t, e, b, "owner-sub")
	_, token, _ := mint(t, b, csrf, `{"scopes":["policy:write"]}`)
	cli := e.browser(t)
	if status, _, _ := mint(t, cli, bearer(token), `{"scopes":["policy:write"]}`); status != http.StatusForbidden {
		t.Fatalf("a token minted a token: %d", status)
	}
	actor, _ := cli.actor(t, http.MethodGet, bearer(token))
	if d := e.authorize(t, actor, policySpec, ""); d.Effect != core.EffectRequireStepUp {
		t.Fatalf("a token carried the session's step-up: %+v", d)
	}
	if n := e.svc.RevokeActor(actor.ID); n != 2 {
		t.Fatalf("RevokeActor ended %d credentials, want the session and the token", n)
	}
	if _, code := cli.actor(t, http.MethodGet, bearer(token)); code != http.StatusUnauthorized {
		t.Fatalf("revoking the actor left its token alive: %d", code)
	}
}

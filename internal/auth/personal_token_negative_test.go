package auth_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/pkg/core"
)

// signedIn signs subject in with Google and returns the session's CSRF token.
func signedIn(t *testing.T, b *browser, subject string) string {
	t.Helper()
	b.signIn(t, "google", subject)
	view, _ := b.session(t)
	return view.CSRFToken
}

// stepUpFor completes a step-up of b's session; query names the call it is for.
func stepUpFor(t *testing.T, b *browser, subject string, csrf map[string]string, query string) {
	t.Helper()
	_, providerURL := b.hop(t, http.MethodPost, "/auth/step-up/google"+query, csrf)
	if resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, subject, providerURL), nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("step-up: %d", resp.StatusCode)
	}
}

// patForm is the token form for a token named name, living ttlSeconds (zero
// for the default), with scopes.
func patForm(name string, ttlSeconds int64, scopes ...string) url.Values {
	f := url.Values{"scopes": scopes}
	if name != "" {
		f.Set("name", name)
	}
	if ttlSeconds != 0 {
		f.Set("ttl_seconds", strconv.FormatInt(ttlSeconds, 10))
	}
	return f
}

// stepUpQuery names the confirmation target asks for.
func stepUpQuery(target auth.StepUpTarget) string {
	return "?" + url.Values{auth.StepUpActionField: {target.Action}, auth.StepUpTargetField: {target.Target}}.Encode()
}

// confirmToken completes a step-up for exactly the token form describes.
func confirmToken(t *testing.T, b *browser, subject, csrf string, form url.Values) {
	t.Helper()
	ttl, _ := strconv.ParseInt(form.Get("ttl_seconds"), 10, 64)
	req := auth.PATRequest{Name: form.Get("name"), Scopes: form["scopes"], TTL: time.Duration(ttl) * time.Second}
	stepUpFor(t, b, subject, map[string]string{auth.CSRFHeader: csrf}, stepUpQuery(req.StepUpTarget()))
}

// mint posts the token form as the person's own navigation, with csrf in
// the form when it is set, and returns the status and the token. header
// adds to or overrides the request headers; an empty value removes one.
func mint(t *testing.T, b *browser, csrf string, form url.Values, header map[string]string) (int, string, auth.TokenInfo) {
	t.Helper()
	body := url.Values{}
	for k, v := range form {
		body[k] = v
	}
	if csrf != "" {
		body.Set(auth.CSRFFormField, csrf)
	}
	h := navigatedFormHeaders()
	for k, v := range header {
		if v == "" {
			delete(h, k)
			continue
		}
		h[k] = v
	}
	resp := b.do(t, http.MethodPost, "/auth/tokens", body.Encode(), h)
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

// mintConfirmed confirms exactly the token form describes, then mints it.
func mintConfirmed(t *testing.T, b *browser, subject, csrf string, form url.Values) (int, string, auth.TokenInfo) {
	t.Helper()
	confirmToken(t, b, subject, csrf, form)
	return mint(t, b, csrf, form, nil)
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func TestMintingAPersonalTokenNeedsAFreshStepUp(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	csrf := signedIn(t, b, "owner-sub")
	if status, _, _ := mint(t, b, csrf, patForm("", 0, "prs:write"), nil); status != http.StatusForbidden {
		t.Fatalf("a token was minted without step-up: %d", status)
	}
	confirmToken(t, b, "owner-sub", csrf, patForm("", 0, "prs:write"))
	if status, _, _ := mint(t, b, "", patForm("", 0, "prs:write"), nil); status != http.StatusForbidden {
		t.Fatalf("a token was minted without a CSRF token: %d", status)
	}
}

func TestPersonalTokensAreLimitedToTheirScopes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	csrf := signedIn(t, b, "owner-sub")
	status, token, info := mintConfirmed(t, b, "owner-sub", csrf, patForm("cli", 3600, "prs:write"))
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
	for name, form := range map[string]url.Values{
		"no scopes": patForm("", 0), "wildcard": patForm("", 0, "*"), "repeated": patForm("", 0, "a:b", "a:b"),
		"too long-lived": patForm("", 691200, "prs:write"), "negative ttl": patForm("", -1, "prs:write"),
		"unknown field": {"scopes": {"prs:write"}, "admin": {"true"}}, "two names": {"scopes": {"prs:write"}, "name": {"a", "b"}},
	} {
		if status, _, _ := mint(t, b, csrf, form, nil); status != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, status)
		}
	}
}

func TestPersonalTokensExpireAndCanBeRevoked(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	csrf := signedIn(t, b, "owner-sub")
	_, short, _ := mintConfirmed(t, b, "owner-sub", csrf, patForm("", 60, "prs:write"))
	_, revoked, info := mintConfirmed(t, b, "owner-sub", csrf, patForm("", 0, "prs:write"))
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
	if resp := b.do(t, http.MethodDelete, "/auth/tokens/"+info.ID, "", map[string]string{auth.CSRFHeader: csrf}); resp.StatusCode != http.StatusNoContent {
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
	csrf := signedIn(t, b, "owner-sub")
	_, token, _ := mintConfirmed(t, b, "owner-sub", csrf, patForm("", 0, "policy:write"))
	cli := e.browser(t)
	if status, _, _ := mint(t, cli, "", patForm("", 0, "policy:write"), bearer(token)); status != http.StatusForbidden {
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

// navigatedFormHeaders are the headers a browser sends with a form a person
// submits on one of the control plane's pages.
func navigatedFormHeaders() map[string]string {
	return map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Mode": "navigate",
		"Sec-Fetch-Dest": "document", "Sec-Fetch-Site": "same-origin", "Sec-Fetch-User": "?1"}
}

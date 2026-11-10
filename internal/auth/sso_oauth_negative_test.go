package auth_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/testkit/fakeoidc"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
)

// rejectedSignIn runs a Google sign-in with faults active and requires that
// it fails as UNAUTHENTICATED without leaving a session behind.
func rejectedSignIn(t *testing.T, faults fakeoidc.Faults) {
	t.Helper()
	e := newEnv(t)
	e.fake.SetFaults(faults)
	b := e.browser(t)
	resp := b.signIn(t, "google", "owner-sub")
	body := readAll(t, resp.Body)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, string(core.CodeUnauthenticated)) {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookie && c.Value != "" {
			t.Fatal("a rejected sign-in set a session cookie")
		}
	}
	if _, ok := b.session(t); ok {
		t.Fatal("a rejected sign-in left a session")
	}
}

func TestSignInRejectsAWrongIssuer(t *testing.T) {
	t.Parallel()
	rejectedSignIn(t, fakeoidc.Faults{Issuer: "https://accounts.evil.example"})
}

func TestProviderDiscoveryRejectsAMismatchedIssuer(t *testing.T) {
	t.Parallel()
	fake := fakeoidc.NewTestServer(t)
	_, err := auth.NewOIDCProvider(context.Background(), auth.OIDCConfig{Name: "google", Issuer: fake.Issuer() + "/",
		ClientID: "sisf", RedirectURL: "http://127.0.0.1:1/auth/callback/google", Clock: core.SystemClock{}})
	if err == nil {
		t.Fatal("a provider reporting another issuer was trusted")
	}
}

func TestSignInRejectsAWrongAudience(t *testing.T) {
	t.Parallel()
	rejectedSignIn(t, fakeoidc.Faults{Audience: "another-client"})
}

func TestSignInRejectsAWrongNonce(t *testing.T) {
	t.Parallel()
	rejectedSignIn(t, fakeoidc.Faults{Nonce: "nonce-from-another-sign-in"})
}

func TestSignInRejectsAnExpiredToken(t *testing.T) {
	t.Parallel()
	rejectedSignIn(t, fakeoidc.Faults{Lifetime: -time.Second})
}

func TestSignInRejectsATokenSignedWithAnUnpublishedKey(t *testing.T) {
	t.Parallel()
	rejectedSignIn(t, fakeoidc.Faults{UnknownKey: true})
}

func TestSignInStateIsSingleUseAndBoundToTheBrowser(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	victim, attacker := e.browser(t), e.browser(t)
	// Login CSRF: the attacker's callback URL, opened in the victim's browser.
	callback := attacker.atProvider(t, "mallory-sub", attacker.toProvider(t, "google"))
	if resp, _ := victim.hop(t, http.MethodGet, callback, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a sign-in finished in another browser: %d", resp.StatusCode)
	}
	if resp, _ := attacker.hop(t, http.MethodGet, callback, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a consumed state was accepted again: %d", resp.StatusCode)
	}
	b := e.browser(t)
	providerURL := b.toProvider(t, "google")
	e.clock.Advance(11 * time.Minute)
	if resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, "owner-sub", providerURL), nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an expired sign-in was accepted: %d", resp.StatusCode)
	}
}

func TestAnEmailMatchAloneNeverEstablishesIdentity(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// mallory has the Owner's verified email at the same provider, and the
	// Owner's email is also the GitHub email of nobody in particular.
	for provider, subject := range map[string]string{"google": "mallory-sub", "github": "owner-sub"} {
		b := e.browser(t)
		if resp := b.signIn(t, provider, subject); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s sign-in: %d", provider, resp.StatusCode)
		}
		view, _ := b.session(t)
		if view.ActorID == e.actorID("owner-sub") {
			t.Fatalf("%s: the same email made the Owner's actor", provider)
		}
		actor, _ := b.actor(t, http.MethodGet, nil)
		actor.StepUpAt = e.clock.Now()
		if d := e.authorize(t, actor, policySpec, ""); d.Effect != core.EffectDeny {
			t.Fatalf("%s: an email match got the Owner role: %+v", provider, d)
		}
	}
	if auth.ActorID(identity.Identity{Issuer: e.fake.Issuer(), Subject: "x", Email: "owner@example.test"}) == e.actorID("owner-sub") {
		t.Fatal("the actor ID depends on email")
	}
}

func TestStateChangingRequestsNeedTheSessionCSRFToken(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b, other := e.browser(t), e.browser(t)
	b.signIn(t, "google", "owner-sub")
	other.signIn(t, "google", "alice-sub")
	mine, _ := b.session(t)
	theirs, _ := other.session(t)
	for name, header := range map[string]map[string]string{
		"missing": nil, "empty": {auth.CSRFHeader: ""}, "another session's": {auth.CSRFHeader: theirs.CSRFToken},
	} {
		if _, status := b.actor(t, http.MethodPost, header); status != http.StatusForbidden {
			t.Errorf("%s CSRF token: status %d", name, status)
		}
	}
	if _, status := b.actor(t, http.MethodGet, nil); status != http.StatusOK {
		t.Fatalf("a safe request needs no CSRF token: %d", status)
	}
	if _, status := b.actor(t, http.MethodPost, map[string]string{auth.CSRFHeader: mine.CSRFToken}); status != http.StatusOK {
		t.Fatalf("the right CSRF token was refused: %d", status)
	}
	form := b.do(t, http.MethodPost, "/api/whoami", auth.CSRFFormField+"="+mine.CSRFToken,
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if form.StatusCode != http.StatusOK {
		t.Fatalf("a form POST with the CSRF field was refused: %d", form.StatusCode)
	}
	if resp := b.do(t, http.MethodPost, "/auth/logout", "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("logout without a CSRF token: %d", resp.StatusCode)
	}
}

func TestARevokedSessionStopsWorkingAtOnce(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	b.signIn(t, "google", "owner-sub")
	actor, _ := b.actor(t, http.MethodGet, nil)
	if d := e.authorize(t, actor, mergeSpec, ""); !d.Allowed() {
		t.Fatalf("setup: %+v", d)
	}
	if err := e.svc.RevokeSession(actor.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, status := b.actor(t, http.MethodGet, nil); status != http.StatusUnauthorized {
		t.Fatalf("a revoked session cookie still authenticates: %d", status)
	}
	// An actor authenticated before the revocation is denied at action time.
	if d := e.authorize(t, actor, mergeSpec, ""); d.Effect != core.EffectDeny {
		t.Fatalf("a revoked session still acts: %+v", d)
	}
	if err := e.svc.RevokeSession(actor.SessionID); !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("revoking twice: %v", err)
	}
	b.signIn(t, "google", "owner-sub")
	again, _ := b.actor(t, http.MethodGet, nil)
	if n := e.svc.RevokeActor(again.ID); n != 1 {
		t.Fatalf("RevokeActor ended %d credentials", n)
	}
	if _, status := b.actor(t, http.MethodGet, nil); status != http.StatusUnauthorized {
		t.Fatalf("revoking the actor left the session alive: %d", status)
	}
	b.signIn(t, "google", "owner-sub")
	e.clock.Advance(auth.DefaultSessionTTL)
	if _, status := b.actor(t, http.MethodGet, nil); status != http.StatusUnauthorized {
		t.Fatalf("an expired session authenticates: %d", status)
	}
}

func TestACrossRepositoryInstallationGrantsNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// alice maintains acme/app and administers acme/other on GitHub. The
	// factory is installed on acme/app only.
	e.fake.SetRepoPermission("acme/app", "alice", "maintain")
	e.fake.SetRepoPermission("acme/other", "alice", "admin")
	b := e.browser(t)
	b.signIn(t, "github", "alice-sub")
	actor, _ := b.actor(t, http.MethodGet, nil)
	if d := e.authorize(t, actor, mergeSpec, "acme/app#4"); !d.Allowed() {
		t.Fatalf("installed repository: %+v", d)
	}
	for _, resource := range []string{"acme/other#4", "repo:acme/other"} {
		if d := e.authorize(t, actor, mergeSpec, resource); d.Effect != core.EffectDeny {
			t.Fatalf("%s: a login reached a repository the factory is not installed on: %+v", resource, d)
		}
	}
}

func TestSensitiveActionsRequireAFreshStepUp(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	b.signIn(t, "google", "owner-sub")
	view, _ := b.session(t)
	actor, _ := b.actor(t, http.MethodGet, nil)
	if d := e.authorize(t, actor, policySpec, ""); d.Effect != core.EffectRequireStepUp {
		t.Fatalf("an ambient session passed step-up: %+v", d)
	}
	csrf := map[string]string{auth.CSRFHeader: view.CSRFToken}
	if resp := b.do(t, http.MethodPost, "/auth/step-up/google", "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("step-up started without a CSRF token: %d", resp.StatusCode)
	}
	resp, providerURL := b.hop(t, http.MethodPost, "/auth/step-up/google?return_to=/approvals", csrf)
	if q := query(t, providerURL); resp.StatusCode != http.StatusSeeOther || q.Get("prompt") != "login" || q.Get("max_age") != "0" {
		t.Fatalf("step-up must force a fresh sign-in: %d %v", resp.StatusCode, q)
	}
	e.clock.Advance(time.Minute)
	if resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, "owner-sub", providerURL), nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("step-up callback: %d %s", resp.StatusCode, readAll(t, resp.Body))
	}
	actor, _ = b.actor(t, http.MethodGet, nil)
	if !actor.StepUpAt.Equal(e.clock.Now()) {
		t.Fatalf("StepUpAt = %v", actor.StepUpAt)
	}
	if d := e.authorize(t, actor, policySpec, ""); !d.Allowed() {
		t.Fatalf("a fresh step-up was refused: %+v", d)
	}
	e.clock.Advance(6 * time.Minute)
	if d := e.authorize(t, actor, policySpec, ""); d.Effect != core.EffectRequireStepUp {
		t.Fatalf("a stale step-up was accepted: %+v", d)
	}
}

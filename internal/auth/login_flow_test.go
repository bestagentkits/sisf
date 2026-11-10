package auth_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
)

func TestGoogleSignInOpensAHardenedSession(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	providerURL := b.toProvider(t, "google")
	q := query(t, providerURL)
	if q.Get("nonce") == "" || q.Get("state") == "" || q.Get("code_challenge_method") != "S256" || q.Get("prompt") != "" {
		t.Fatalf("authorization request = %v", q)
	}
	resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, "owner-sub", providerURL), nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/dashboard" {
		t.Fatalf("callback: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookie && c.Value != "" {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("session cookie = %+v", cookie)
	}
	view, ok := b.session(t)
	if !ok || view.ActorID != e.actorID("owner-sub") || view.CSRFToken == "" || !strings.HasPrefix(view.SessionID, "ses_") {
		t.Fatalf("session = %+v %v", view, ok)
	}
	if strings.Contains(view.SessionID, cookie.Value) || strings.Contains(view.CSRFToken, cookie.Value) {
		t.Fatal("the session view exposes the cookie value")
	}
	actor, status := b.actor(t, http.MethodGet, nil)
	if status != http.StatusOK || actor.ID != view.ActorID || actor.Kind != core.ActorHuman || actor.Surface != core.SurfaceAPI ||
		len(actor.Roles) != 0 || !actor.StepUpAt.IsZero() {
		t.Fatalf("actor = %+v (%d)", actor, status)
	}
}

func TestGitHubSignInIsKeyedByUserIDAndGrantsNoRepositoryAccessByItself(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	if resp := b.signIn(t, "github", "alice-sub"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback: %d %s", resp.StatusCode, readAll(t, resp.Body))
	}
	view, _ := b.session(t)
	want := auth.ActorID(identity.Identity{Issuer: e.githubIssuer, Subject: "1002"})
	if view.ActorID != want {
		t.Fatalf("actor %s, want the GitHub user ID as subject (%s)", view.ActorID, want)
	}
	id, ok := e.svc.Identity(want)
	if !ok || id.Login != "alice" || id.EmailVerified {
		t.Fatalf("identity = %+v; GitHub email is never treated as verified", id)
	}
	actor, _ := b.actor(t, http.MethodGet, nil)
	// No GitHub permission on the repository yet: signed in, but no role.
	if d := e.authorize(t, actor, mergeSpec, ""); d.Effect != core.EffectDeny {
		t.Fatalf("a bare GitHub login was given a role: %+v", d)
	}
	e.fake.SetRepoPermission("acme/app", "alice", "maintain")
	if d := e.authorize(t, actor, mergeSpec, ""); !d.Allowed() {
		t.Fatalf("a GitHub maintainer was refused: %+v", d)
	}
}

func TestSignOutEndsTheSession(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	b.signIn(t, "google", "owner-sub")
	view, _ := b.session(t)
	resp := b.do(t, http.MethodPost, "/auth/logout", "", map[string]string{auth.CSRFHeader: view.CSRFToken})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if _, ok := b.session(t); ok {
		t.Fatal("still signed in after logout")
	}
}

func TestReturnAddressStaysOnSite(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, target := range []string{"https://evil.example/", "//evil.example/x", "/\\evil.example", "javascript:alert(1)"} {
		b := e.browser(t)
		_, providerURL := b.hop(t, http.MethodGet, "/auth/login/google?return_to="+target, nil)
		resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, "owner-sub", providerURL), nil)
		if loc := resp.Header.Get("Location"); loc != "/" {
			t.Errorf("return_to %q redirected to %q", target, loc)
		}
	}
	b := e.browser(t)
	if resp, _ := b.hop(t, http.MethodGet, "/auth/login/okta", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown provider: %d", resp.StatusCode)
	}
}

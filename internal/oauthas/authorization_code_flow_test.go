package oauthas_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/oauthas"
	"github.com/bestagentkits/sisf/internal/oauthas/oauthastest"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestSignedInPersonAuthorizesClientAndTokenStandsForThem(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	p := oauthastest.NewPKCE(t)
	tok := e.Exchange(t, e.ExchangeForm(codeFor(t, e, "owner-sub", p), p))
	if tok.Status != http.StatusOK || tok.TokenType != "Bearer" || tok.ExpiresIn != int64(oauthas.DefaultAccessTokenTTL.Seconds()) {
		t.Fatalf("token response: %+v", tok)
	}
	if tok.Scope != "policy:write runs:read" {
		t.Fatalf("an Owner asking for nothing in particular gets the scopes they hold, got %q", tok.Scope)
	}
	actor, err := verify(e, tok.AccessToken)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if actor.ID != e.ActorID("owner-sub") || actor.Kind != core.ActorHuman || actor.SessionID == "" ||
		!slices.Equal(actor.Scopes, []string{"policy:write", "runs:read"}) {
		t.Fatalf("actor = %+v", actor)
	}
	if !actor.StepUpAt.IsZero() || len(actor.Roles) != 0 {
		t.Fatalf("a token must carry neither a step-up nor roles: %+v", actor)
	}
}

func TestPersonWithoutSessionIsSentToSignInAndBack(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.Browser(t)
	q := e.AuthorizeQuery(oauthastest.NewPKCE(t))
	resp := authorize(t, e, b, q)
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || err != nil || loc.Path != oauthastest.LoginPath {
		t.Fatalf("expected a redirect to sign in, got %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if back := loc.Query().Get("return_to"); back != oauthas.AuthorizePath+"?"+q.Encode() {
		t.Fatalf("return_to = %q", back)
	}
	b.SignIn(t, "viewer-sub")
	page := authorize(t, e, b, q)
	if page.StatusCode != http.StatusOK || page.Header.Get("X-Frame-Options") != "DENY" ||
		!strings.Contains(page.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("consent page: %d %v", page.StatusCode, page.Header)
	}
}

func TestDeclinedConsentReturnsAccessDeniedAndNoCode(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := signedIn(t, e, "viewer-sub")
	resp := b.Decide(t, authorize(t, e, b, e.AuthorizeQuery(oauthastest.NewPKCE(t))), "deny")
	if got := redirectError(t, e, resp); got != "access_denied" {
		t.Fatalf("error = %q", got)
	}
}

func TestMetadataDocumentsAdvertiseOnlyTheSafeFlow(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var as oauthas.ServerMetadata
	getJSON(t, e, oauthas.AuthorizationServerMetadataPath, &as)
	if as.Issuer != e.Base || as.AuthorizationEndpoint != e.Base+oauthas.AuthorizePath || as.TokenEndpoint != e.Base+oauthas.TokenPath ||
		as.RegistrationEndpoint != e.Base+oauthas.RegisterPath || !as.AuthorizationResponseIssParameter {
		t.Fatalf("server metadata: %+v", as)
	}
	for name, got := range map[string][]string{"response types": as.ResponseTypesSupported, "grants": as.GrantTypesSupported,
		"PKCE methods": as.CodeChallengeMethodsSupported, "client auth": as.TokenEndpointAuthMethodsSupported} {
		if len(got) != 1 || slices.Contains(got, "token") || slices.Contains(got, "plain") || slices.Contains(got, "client_secret_basic") {
			t.Errorf("%s advertises %v", name, got)
		}
	}
	if e.Server.ProtectedResourceMetadataURL() != e.Base+"/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("resource metadata URL = %s", e.Server.ProtectedResourceMetadataURL())
	}
	var rm oauthas.ResourceMetadata
	getJSON(t, e, "/.well-known/oauth-protected-resource/mcp", &rm)
	if rm.Resource != e.Base+oauthastest.ResourcePath || !slices.Equal(rm.AuthorizationServers, []string{e.Base}) ||
		!slices.Equal(rm.ScopesSupported, []string{"policy:write", "runs:read"}) || !slices.Equal(rm.BearerMethodsSupported, []string{"header"}) {
		t.Fatalf("resource metadata: %+v", rm)
	}
}

func getJSON(t *testing.T, e *oauthastest.Env, path string, v any) {
	t.Helper()
	resp, err := e.HTTP.Get(e.Base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

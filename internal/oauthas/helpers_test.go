package oauthas_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/bestagentkits/sisf/internal/oauthas"
	"github.com/bestagentkits/sisf/internal/oauthas/oauthastest"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The actions whose scopes people may delegate in these tests.
var (
	readSpec = core.ActionSpec{Name: "runs.inspect", Summary: "read a run", Sensitivity: core.SensitivityRead,
		Scopes: []string{"runs:read"}, Roles: []core.Role{core.RoleViewer, core.RoleMaintainer, core.RoleOwner}}
	policySpec = core.ActionSpec{Name: "policy.update", Summary: "change policy", Sensitivity: core.SensitivitySensitive,
		StepUp: true, Scopes: []string{"policy:write"}, Roles: []core.Role{core.RoleOwner}}
)

// newEnv starts an environment where owner-sub is the Owner and viewer-sub a
// Viewer.
func newEnv(t *testing.T, configure ...func(*oauthas.Config)) *oauthastest.Env {
	t.Helper()
	opts := oauthastest.Options{Specs: []core.ActionSpec{readSpec, policySpec}}
	if len(configure) > 0 {
		opts.Configure = configure[0]
	}
	e := oauthastest.New(t, opts)
	e.Grant(t, "owner-sub", core.RoleOwner)
	e.Grant(t, "viewer-sub", core.RoleViewer)
	return e
}

// signedIn returns a browser in which subject is signed in.
func signedIn(t *testing.T, e *oauthastest.Env, subject string) *oauthastest.Browser {
	t.Helper()
	b := e.Browser(t)
	b.SignIn(t, subject)
	return b
}

// authorize sends the authorization request q from b.
func authorize(t *testing.T, e *oauthastest.Env, b *oauthastest.Browser, q url.Values) *http.Response {
	t.Helper()
	return b.Do(t, http.MethodGet, oauthas.AuthorizePath+"?"+q.Encode(), nil, nil)
}

// codeFor runs a signed-in authorization for subject and returns the code.
func codeFor(t *testing.T, e *oauthastest.Env, subject string, p oauthastest.PKCE, scopes ...string) string {
	t.Helper()
	b := signedIn(t, e, subject)
	return oauthastest.Code(t, b.Decide(t, authorize(t, e, b, e.AuthorizeQuery(p, scopes...)), "approve"))
}

// redirectError returns the error code a redirect to the client carries,
// failing unless resp is such a redirect to the registered URI.
func redirectError(t *testing.T, e *oauthastest.Env, resp *http.Response) string {
	t.Helper()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || err != nil {
		t.Fatalf("expected a redirect, got %d", resp.StatusCode)
	}
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != e.RedirectURI {
		t.Fatalf("redirected to %s, want the registered URI", got)
	}
	if loc.Query().Get("code") != "" {
		t.Fatal("an error redirect carried a code")
	}
	if loc.Query().Get("iss") != e.Base || loc.Query().Get("state") != "st-123" {
		t.Fatalf("error redirect lacks iss or state: %s", loc.RawQuery)
	}
	return loc.Query().Get("error")
}

// verify checks raw at the resource side of e.
func verify(e *oauthastest.Env, raw string) (core.Actor, error) {
	return e.Server.VerifyAccessToken(context.Background(), raw)
}

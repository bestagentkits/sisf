package control_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/actions/pendingactions"
	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/internal/oauthas"
	"github.com/bestagentkits/sisf/internal/testkit/fakeoidc"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// externalURL is where the control server of the tests says it is reached.
const (
	externalURL  = "http://127.0.0.1:7463"
	oidcSecret   = "control-test-oidc-secret"
	ownerSubject = "owner-sub"
)

// allowAll lets every authenticated caller run every action; these tests
// are about routing, not authorization.
type allowAll struct{}

func (allowAll) Authorize(context.Context, core.AuthzRequest) (core.AuthzDecision, error) {
	return core.AuthzDecision{Effect: core.EffectAllow}, nil
}

// surfaces builds every surface over the stand-in actions, with sign-in at a
// fake identity provider.
func newSurfaces(t *testing.T, clock *sqlstoretest.Clock) (control.Surfaces, *fakeoidc.Server) {
	t.Helper()
	fake := fakeoidc.NewTestServer(t, fakeoidc.WithClock(clock))
	callback := externalURL + "/auth/callback/google"
	fake.AddClient(fakeoidc.Client{ID: "sisf", Secret: oidcSecret, RedirectURIs: []string{callback}})
	fake.AddUser(fakeoidc.User{Subject: ownerSubject, Email: "owner@example.test", EmailVerified: true})
	google, err := auth.NewOIDCProvider(context.Background(), auth.OIDCConfig{Name: "google", Issuer: fake.Issuer(), ClientID: "sisf",
		ClientSecret: core.NewSecretValue([]byte(oidcSecret)), RedirectURL: callback, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	identitySvc, err := auth.New(auth.Config{Providers: []identity.Provider{google}, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	keys, audit := intaketest.NewKeys("approval-key"), &intaketest.AuditLog{}
	store, err := approvals.NewStore(approvals.Config{State: intaketest.NewState(clock), FactoryID: sqlstoretest.Factory,
		Signer: keys, Verifier: keys, Audit: audit, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	reg := actions.NewRegistry()
	if err := reg.RegisterFallback(pendingactions.Fallbacks()...); err != nil {
		t.Fatal(err)
	}
	svc, err := actions.NewService(actions.Config{FactoryID: sqlstoretest.Factory, Registry: reg, Authorizer: allowAll{},
		Approvals: store, Policy: actions.PolicyRevisionFunc(func(context.Context) (core.CommitSHA, error) {
			return "1111111111111111111111111111111111111111", nil
		}), Audit: audit, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	oauth, err := oauthas.New(oauthas.Config{Issuer: externalURL, Resource: externalURL + "/mcp", LoginPath: "/signin",
		Sessions: identitySvc, Scopes: oauthas.RoleScopes{Roles: func(context.Context, core.Actor) ([]core.Role, error) { return nil, nil },
			Specs: svc.Specs}, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	return control.Surfaces{Identity: identitySvc, Actions: svc, OAuth: oauth, LoginProviders: []string{"google"},
		ExternalURL: externalURL}, fake
}

// signIn runs the sign-in flow of subject against the handler and returns
// the session cookie.
func (e *env) signIn(t *testing.T, subject string) *http.Cookie {
	t.Helper()
	start := e.do(httptest.NewRequest(http.MethodGet, "/auth/login/google?return_to=/", nil))
	if start.Code != http.StatusFound {
		t.Fatalf("sign-in start = %d", start.Code)
	}
	if !e.fake.SignInAs(subject) {
		t.Fatalf("no user %s", subject)
	}
	client := &http.Client{Transport: &http.Transport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || back.Query().Get("code") == "" {
		t.Fatalf("the provider did not send a code back: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	req := httptest.NewRequest(http.MethodGet, back.RequestURI(), nil)
	for _, c := range start.Result().Cookies() {
		req.AddCookie(c)
	}
	done := e.do(req)
	for _, c := range done.Result().Cookies() {
		if c.Name == auth.SessionCookie && c.Value != "" {
			return c
		}
	}
	t.Fatalf("the callback set no session: %d %s", done.Code, done.Body.String())
	return nil
}

// Package oauthastest runs the authorization server in front of the real
// authentication service and a fake identity provider, on ephemeral ports,
// and drives the browser and client sides of the authorization code flow.
// Tests of the authorization server and of the MCP endpoint share it.
package oauthastest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/internal/oauthas"
	"github.com/bestagentkits/sisf/internal/rbac"
	"github.com/bestagentkits/sisf/internal/testkit/fakeoidc"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
)

// Fixed names of the test environment.
const (
	ClientID     = "test-client"
	ResourcePath = "/mcp"
	LoginPath    = "/auth/login/google"
	provider     = "google"
	rpSecret     = "rp-client-secret"
)

// Start is the instant the environment's clock starts at.
var Start = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

// Options shape an environment. Every field is optional.
type Options struct {
	// Specs are the actions whose scopes people may delegate; their roles
	// decide who holds which scope.
	Specs []core.ActionSpec
	// Scopes replaces the role-based scope policy.
	Scopes oauthas.ScopePolicy
	// Mount adds routes, such as the MCP endpoint, before the server starts.
	Mount func(mux *http.ServeMux, e *Env)
	// Configure edits the authorization server's configuration.
	Configure func(cfg *oauthas.Config)
}

// Env is a running factory front door.
type Env struct {
	Clock    *intaketest.Clock
	Provider *fakeoidc.Server
	Auth     *auth.Service
	Grants   *rbac.Grants
	Server   *oauthas.Server
	// Base is the origin every route is served on; it is also the issuer.
	Base string
	// RedirectURI is the registered redirect URI of ClientID. Nothing listens
	// there: the flow reads the redirect instead of following it.
	RedirectURI string
	// HTTP is a client that does not follow redirects.
	HTTP *http.Client
}

// New starts an environment and stops it when the test ends.
func New(t testing.TB, opts Options) *Env {
	t.Helper()
	e := &Env{Clock: intaketest.NewClock(Start), Grants: rbac.NewGrants(), RedirectURI: "http://127.0.0.1:9/callback",
		HTTP: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	e.Provider = fakeoidc.NewTestServer(t, fakeoidc.WithClock(e.Clock))
	ts := httptest.NewUnstartedServer(nil)
	e.Base = "http://" + ts.Listener.Addr().String()
	callback := e.Base + "/auth/callback/" + provider
	e.Provider.AddClient(fakeoidc.Client{ID: "sisf", Secret: rpSecret, RedirectURIs: []string{callback}})
	for _, sub := range []string{"owner-sub", "alice-sub", "viewer-sub", "nobody-sub"} {
		e.Provider.AddUser(fakeoidc.User{Subject: sub, Email: sub + "@example.test", EmailVerified: true})
	}
	google, err := auth.NewOIDCProvider(context.Background(), auth.OIDCConfig{Name: provider, Issuer: e.Provider.Issuer(),
		ClientID: "sisf", ClientSecret: core.NewSecretValue([]byte(rpSecret)), RedirectURL: callback, Clock: e.Clock})
	if err != nil {
		t.Fatalf("identity provider: %v", err)
	}
	if e.Auth, err = auth.New(auth.Config{Providers: []identity.Provider{google}, Clock: e.Clock}); err != nil {
		t.Fatalf("authentication service: %v", err)
	}
	policy := opts.Scopes
	if policy == nil {
		policy = oauthas.RoleScopes{
			Roles: func(_ context.Context, a core.Actor) ([]core.Role, error) { return e.Grants.Roles(a.ID, a.Kind), nil },
			Specs: func() []core.ActionSpec { return opts.Specs },
		}
	}
	cfg := oauthas.Config{Issuer: e.Base, Resource: e.Base + ResourcePath, ResourceName: "test factory",
		LoginPath: LoginPath, Sessions: e.Auth, Scopes: policy, ScopesSupported: oauthas.ScopesOf(opts.Specs),
		Clients:             []oauthas.Client{{ID: ClientID, Name: "Test client", RedirectURIs: []string{e.RedirectURI}}},
		DynamicRegistration: true, Clock: e.Clock}
	if opts.Configure != nil {
		opts.Configure(&cfg)
	}
	if e.Server, err = oauthas.New(cfg); err != nil {
		t.Fatalf("authorization server: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/auth/", e.Auth.Handler())
	e.Server.Mount(mux)
	if opts.Mount != nil {
		opts.Mount(mux, e)
	}
	ts.Config.Handler = mux
	ts.Start()
	t.Cleanup(ts.Close)
	return e
}

// ActorID is the actor of subject at the fake provider.
func (e *Env) ActorID(subject string) string {
	return auth.ActorID(identity.Identity{Issuer: e.Provider.Issuer(), Subject: subject})
}

// Grant gives subject's person role.
func (e *Env) Grant(t testing.TB, subject string, role core.Role) {
	t.Helper()
	if err := e.Grants.Add(e.ActorID(subject), core.ActorHuman, role); err != nil {
		t.Fatalf("grant %s: %v", role, err)
	}
}

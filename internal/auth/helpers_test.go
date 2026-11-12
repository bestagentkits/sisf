package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/rbac"
	"github.com/bestagentkits/sisf/internal/testkit/fakeoidc"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

const clientSecret = "rp-client-secret"

// env is a factory front door: the auth service and authorizer, served over
// HTTP on an ephemeral port, signing people in at a fake provider.
type env struct {
	clock  *sqlstoretest.Clock
	fake   *fakeoidc.Server
	svc    *auth.Service
	authz  *rbac.Authorizer
	grants *rbac.Grants
	base   string
	// githubIssuer is the issuer of GitHub-style identities.
	githubIssuer string
}

// newEnv builds the front door; tweak adjusts the service's configuration.
func newEnv(t *testing.T, tweak ...func(e *env, cfg *auth.Config)) *env {
	t.Helper()
	e := &env{clock: sqlstoretest.NewClock(time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)), grants: rbac.NewGrants()}
	e.fake = fakeoidc.NewTestServer(t, fakeoidc.WithClock(e.clock))
	e.githubIssuer = e.fake.Issuer() + "/github"
	ts := httptest.NewUnstartedServer(nil)
	e.base = "http://" + ts.Listener.Addr().String()
	e.fake.AddClient(fakeoidc.Client{ID: "sisf", Secret: clientSecret,
		RedirectURIs: []string{e.base + "/auth/callback/google", e.base + "/auth/callback/github"}})
	for _, u := range []fakeoidc.User{
		{Subject: "owner-sub", Email: "owner@example.test", EmailVerified: true, Login: "owner", GitHubID: 1001},
		{Subject: "alice-sub", Email: "alice@example.test", EmailVerified: true, Login: "alice", GitHubID: 1002},
		{Subject: "mallory-sub", Email: "owner@example.test", EmailVerified: true, Login: "mallory", GitHubID: 1003},
	} {
		e.fake.AddUser(u)
	}
	google, err := auth.NewOIDCProvider(context.Background(), auth.OIDCConfig{Name: "google", Issuer: e.fake.Issuer(),
		ClientID: "sisf", ClientSecret: core.NewSecretValue([]byte(clientSecret)), RedirectURL: e.base + "/auth/callback/google", Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	github, err := auth.NewGitHubProvider(auth.GitHubConfig{Issuer: e.githubIssuer, AuthURL: e.fake.Issuer() + fakeoidc.GitHubAuthorizePath,
		TokenURL: e.fake.Issuer() + fakeoidc.GitHubTokenPath, APIBase: e.fake.Issuer() + fakeoidc.GitHubAPIPrefix,
		ClientID: "sisf", ClientSecret: core.NewSecretValue([]byte(clientSecret)), RedirectURL: e.base + "/auth/callback/github"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := auth.Config{Providers: []identity.Provider{google, github}, Clock: e.clock}
	for _, f := range tweak {
		f(e, &cfg)
	}
	if e.svc, err = auth.New(cfg); err != nil {
		t.Fatal(err)
	}
	e.authz, err = rbac.New(rbac.Config{Grants: e.grants, Sessions: e.svc, Clock: e.clock, DefaultRepo: "acme/app",
		Installations: installs{"acme/app": {ID: 7, Owner: "acme", Repo: "app"}},
		Membership: &rbac.GitHubMembership{APIBase: e.fake.Issuer() + fakeoidc.GitHubAPIPrefix, Token: func(context.Context, string, string) (core.SecretValue, error) {
			return core.NewSecretValue([]byte("ghs_installation")), nil
		}},
		Accounts: e.svc})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.grants.Add(e.actorID("owner-sub"), core.ActorHuman, core.RoleOwner); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/auth/", e.svc.Handler())
	mux.Handle("/api/", e.svc.Middleware(core.SurfaceAPI, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, _ := auth.ActorFrom(r.Context())
		_ = json.NewEncoder(w).Encode(actor)
	})))
	ts.Config.Handler = mux
	ts.Start()
	t.Cleanup(ts.Close)
	return e
}

// actorID is the actor of a Google-style identity at the fake.
func (e *env) actorID(subject string) string {
	return auth.ActorID(identity.Identity{Issuer: e.fake.Issuer(), Subject: subject})
}

func (e *env) authorize(t *testing.T, actor core.Actor, spec core.ActionSpec, resource string) core.AuthzDecision {
	t.Helper()
	d, err := e.authz.Authorize(context.Background(), core.AuthzRequest{Actor: actor, Action: spec, Resource: resource})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	return d
}

type installs map[string]identity.Installation

func (m installs) Installation(_ context.Context, owner, repo string) (identity.Installation, error) {
	if inst, ok := m[owner+"/"+repo]; ok {
		return inst, nil
	}
	return identity.Installation{}, core.NewError(core.CodeNotFound, "not installed")
}

var policySpec = core.ActionSpec{Name: "policy.update", Sensitivity: core.SensitivitySensitive,
	Roles: []core.Role{core.RoleOwner}, StepUp: true, Scopes: []string{"policy:write"}}

var mergeSpec = core.ActionSpec{Name: "prs.merge", Sensitivity: core.SensitivityWrite,
	Roles: []core.Role{core.RoleMaintainer, core.RoleOwner}, Scopes: []string{"prs:write"}}

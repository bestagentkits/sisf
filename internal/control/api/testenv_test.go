package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/control/api"
	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/internal/testkit/fakeoidc"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
)

const (
	testFactory core.FactoryID = "acme-app"
	policySHA   core.CommitSHA = "1111111111111111111111111111111111111111"
	oidcSecret                 = "rp-client-secret"
	leakedText                 = "password=hunter2"
)

// scopedAuthorizer allows everything except an action whose scopes a
// token-bearing actor does not hold; a session actor carries no scopes.
type scopedAuthorizer struct{}

func (scopedAuthorizer) Authorize(_ context.Context, req core.AuthzRequest) (core.AuthzDecision, error) {
	for _, scope := range req.Action.Scopes {
		if len(req.Actor.Scopes) > 0 && !slices.Contains(req.Actor.Scopes, scope) {
			return core.AuthzDecision{Effect: core.EffectDeny, Reason: "scope"}, nil
		}
	}
	return core.AuthzDecision{Effect: core.EffectAllow}, nil
}

type policySource struct{}

func (policySource) PolicyRevision(context.Context) (core.CommitSHA, error) { return policySHA, nil }

// recorder keeps the requests the sample actions received.
type recorder struct {
	mu   sync.Mutex
	seen []core.ActionRequest
}

func (r *recorder) add(req core.ActionRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, req)
}

func (r *recorder) all() []core.ActionRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.seen)
}

// executed counts the calls that ran for real; planning a request asks the
// handler for a dry run, which changes nothing.
func (r *recorder) executed() int {
	return len(slices.DeleteFunc(r.all(), func(req core.ActionRequest) bool { return req.DryRun }))
}

// sampleActions are four actions that exercise the handler: a read, a write
// with a dry run, a sensitive write, and two that fail in ways that must not
// leak.
func sampleActions(rec *recorder) []core.Action {
	roles := []core.Role{core.RoleOwner, core.RoleMaintainer}
	echo := func(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
		rec.add(req)
		if req.DryRun {
			return core.ActionResult{Plan: &core.DryRun{Changes: []core.Change{{Resource: "echo/1", Op: core.ChangeUpdate}}}}, nil
		}
		return actions.JSONResult(map[string]string{"echo": string(req.Input)})
	}
	return []core.Action{
		{Spec: core.ActionSpec{Name: "echo.read", Summary: "Echo.", Sensitivity: core.SensitivityRead, Scopes: []string{"echo:read"}, Roles: roles}, Handle: echo},
		{Spec: core.ActionSpec{Name: "echo.write", Summary: "Echo.", Sensitivity: core.SensitivityWrite, Scopes: []string{"echo:write"}, Roles: roles}, Handle: echo},
		{Spec: core.ActionSpec{Name: "echo.sensitive", Summary: "Echo.", Sensitivity: core.SensitivitySensitive, Scopes: []string{"echo:admin"}, Roles: []core.Role{core.RoleOwner}}, Handle: echo},
		{Spec: core.ActionSpec{Name: "echo.untyped", Summary: "Fails.", Sensitivity: core.SensitivityRead, Scopes: []string{"echo:read"}, Roles: roles},
			Handle: func(context.Context, core.ActionRequest) (core.ActionResult, error) {
				return core.ActionResult{}, errors.New("dial tcp 10.0.0.5: " + leakedText)
			}},
		{Spec: core.ActionSpec{Name: "echo.leaky", Summary: "Fails.", Sensitivity: core.SensitivityRead, Scopes: []string{"echo:read"}, Roles: roles},
			Handle: func(context.Context, core.ActionRequest) (core.ActionResult, error) {
				return core.ActionResult{}, core.NewError(core.CodeInternal, "query failed with "+leakedText).WithCause(errors.New(leakedText))
			}},
	}
}

// env is the REST surface served over HTTP on an ephemeral port, with a real
// authentication service signing people in at a fake provider.
type env struct {
	clock *intaketest.Clock
	fake  *fakeoidc.Server
	auth  *auth.Service
	// api is the REST handler under test.
	api  http.Handler
	rec  *recorder
	base string
	logs *strings.Builder
}

func newEnv(t *testing.T, registry func(*recorder) (*actions.Registry, error), mutate ...func(*api.Config)) *env {
	t.Helper()
	return newEnvWithEvictionPick(t, registry, nil, mutate...)
}

// newEnvWithEvictionPick is newEnv with pick choosing which waiting login
// request to drop when every source holds one; nil means crypto/rand.
func newEnvWithEvictionPick(t *testing.T, registry func(*recorder) (*actions.Registry, error), pick func(n int) (int, error), mutate ...func(*api.Config)) *env {
	t.Helper()
	e := &env{clock: intaketest.NewClock(time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)), rec: &recorder{}, logs: &strings.Builder{}}
	e.fake = fakeoidc.NewTestServer(t, fakeoidc.WithClock(e.clock))
	ts := httptest.NewUnstartedServer(nil)
	e.base = "http://" + ts.Listener.Addr().String()
	e.fake.AddClient(fakeoidc.Client{ID: "sisf", Secret: oidcSecret, RedirectURIs: []string{e.base + "/auth/callback/google", e.base + "/auth/callback/github"}})
	for _, u := range []fakeoidc.User{{Subject: "owner-sub", Email: "owner@example.test", EmailVerified: true, Login: "owner", GitHubID: 1001},
		{Subject: "alice-sub", Email: "alice@example.test", EmailVerified: true, Login: "alice", GitHubID: 1002}} {
		e.fake.AddUser(u)
	}
	google, err := auth.NewOIDCProvider(context.Background(), auth.OIDCConfig{Name: "google", Issuer: e.fake.Issuer(), ClientID: "sisf",
		ClientSecret: core.NewSecretValue([]byte(oidcSecret)), RedirectURL: e.base + "/auth/callback/google", Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	// GitHub signs people in but cannot force a fresh sign-in, so it never
	// confirms anyone.
	github, err := auth.NewGitHubProvider(auth.GitHubConfig{Issuer: e.fake.Issuer() + "/github", AuthURL: e.fake.Issuer() + fakeoidc.GitHubAuthorizePath,
		TokenURL: e.fake.Issuer() + fakeoidc.GitHubTokenPath, APIBase: e.fake.Issuer() + fakeoidc.GitHubAPIPrefix,
		ClientID: "sisf", ClientSecret: core.NewSecretValue([]byte(oidcSecret)), RedirectURL: e.base + "/auth/callback/github"})
	if err != nil {
		t.Fatal(err)
	}
	if e.auth, err = auth.New(auth.Config{Providers: []identity.Provider{google, github}, Clock: e.clock}); err != nil {
		t.Fatal(err)
	}
	reg, err := registry(e.rec)
	if err != nil {
		t.Fatal(err)
	}
	keys, state, audit := intaketest.NewKeys("approval-key"), intaketest.NewState(e.clock), &intaketest.AuditLog{}
	store, err := approvals.NewStore(approvals.Config{State: state, FactoryID: testFactory, Signer: keys, Verifier: keys, Audit: audit, Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := actions.NewService(actions.Config{FactoryID: testFactory, Registry: reg, Authorizer: scopedAuthorizer{},
		Approvals: store, Policy: policySource{}, Audit: audit, Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	cfg := api.Config{Actions: svc, Identity: e.auth, Clock: e.clock, LoginProviders: []string{"google", "github"}, MaxBodyBytes: 2048}
	for _, m := range mutate {
		m(&cfg)
	}
	build := api.New
	if pick != nil {
		build = func(cfg api.Config) (http.Handler, error) { return api.NewWithEvictionPick(cfg, pick) }
	}
	handler, err := build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/auth/", e.auth.Handler())
	mux.Handle(api.MountPath, handler)
	ts.Config.Handler = mux
	ts.Start()
	t.Cleanup(ts.Close)
	return e
}

func sampleRegistry(rec *recorder) (*actions.Registry, error) {
	reg := actions.NewRegistry()
	return reg, reg.Register(sampleActions(rec)...)
}

// token mints a service token for an agent with the given scopes.
func (e *env) token(t *testing.T, scopes ...string) string {
	t.Helper()
	issued, err := e.auth.IssueServiceToken(auth.ServiceTokenRequest{ActorID: "agent:tester", Kind: core.ActorAgent, Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	return string(issued.Token.Reveal())
}

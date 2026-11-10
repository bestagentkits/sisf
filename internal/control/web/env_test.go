package web_test

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/actions/approvalactions"
	"github.com/bestagentkits/sisf/internal/actions/pendingactions"
	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/control/api"
	"github.com/bestagentkits/sisf/internal/control/web"
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
	ownerSub                   = "owner-sub"
	viewerSub                  = "viewer-sub"
)

// roleAuthorizer gives each signed-in subject one role and allows an action
// that names it.
type roleAuthorizer struct{ roles map[string]core.Role }

func (a roleAuthorizer) Authorize(_ context.Context, req core.AuthzRequest) (core.AuthzDecision, error) {
	if !slices.Contains(req.Action.Roles, a.roles[req.Actor.ID]) {
		return core.AuthzDecision{Effect: core.EffectDeny, Reason: "role"}, nil
	}
	return core.AuthzDecision{Effect: core.EffectAllow}, nil
}

// rolesIdentity stands for a wiring that resolves a signed-in person's roles
// before the actor reaches an action, which the approval store needs to see
// the Owner. The authentication service itself returns an actor without roles.
type rolesIdentity struct {
	*auth.Service
	roles map[string]core.Role
}

func (r rolesIdentity) Authenticate(req *http.Request) (core.Actor, error) {
	actor, err := r.Service.Authenticate(req)
	if role, ok := r.roles[actor.ID]; ok && err == nil {
		actor.Roles = []core.Role{role}
	}
	return actor, err
}

type policySource struct{}

func (policySource) PolicyRevision(context.Context) (core.CommitSHA, error) { return policySHA, nil }

// seen records what the in-test actions received.
type seen struct {
	mu    sync.Mutex
	calls []core.ActionRequest
}

func (s *seen) add(req core.ActionRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, req)
}

// ran counts the calls that executed for real; planning asks for a dry run.
func (s *seen) ran(surface core.Surface) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if !c.DryRun && c.Actor.Surface == surface {
			n++
		}
	}
	return n
}

// env is the dashboard, the REST surface and the authentication routes on
// one server, with a real action service behind them.
type env struct {
	clock *intaketest.Clock
	fake  *fakeoidc.Server
	auth  *auth.Service
	svc   *actions.Service
	base  string
	seen  *seen
	state *fixtures
}

func newEnv(t *testing.T, fx *fixtures) *env {
	t.Helper()
	e := &env{clock: intaketest.NewClock(time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)), seen: &seen{}, state: fx}
	e.fake = fakeoidc.NewTestServer(t, fakeoidc.WithClock(e.clock))
	ts := httptest.NewUnstartedServer(nil)
	e.base = "http://" + ts.Listener.Addr().String()
	e.fake.AddClient(fakeoidc.Client{ID: "sisf", Secret: oidcSecret, RedirectURIs: []string{e.base + "/auth/callback/google", e.base + "/auth/callback/github"}})
	for _, u := range []fakeoidc.User{
		{Subject: ownerSub, Email: "owner@example.test", EmailVerified: true, Login: "owner", GitHubID: 1001},
		{Subject: viewerSub, Email: "viewer@example.test", EmailVerified: true, Login: "viewer", GitHubID: 1002}} {
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
	keys, st, audit := intaketest.NewKeys("approval-key"), intaketest.NewState(e.clock), &intaketest.AuditLog{}
	store, err := approvals.NewStore(approvals.Config{State: st, FactoryID: testFactory, Signer: keys, Verifier: keys, Audit: audit, Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	reg := actions.NewRegistry()
	inbox, err := approvalactions.New(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, batch := range [][]core.Action{fx.actions(e.seen), inbox} {
		if err := reg.Register(batch...); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.RegisterFallback(pendingactions.Fallbacks()...); err != nil {
		t.Fatal(err)
	}
	roles := map[string]core.Role{}
	for sub, role := range map[string]core.Role{ownerSub: core.RoleOwner, viewerSub: core.RoleViewer} {
		roles[auth.ActorID(identity.Identity{Issuer: e.fake.Issuer(), Subject: sub})] = role
	}
	roles[auth.ActorID(identity.Identity{Issuer: e.fake.Issuer() + "/github", Subject: "1001"})] = core.RoleOwner
	if e.svc, err = actions.NewService(actions.Config{FactoryID: testFactory, Registry: reg, Authorizer: roleAuthorizer{roles},
		Approvals: store, Policy: policySource{}, Audit: audit, Clock: e.clock, StepUp: e.auth}); err != nil {
		t.Fatal(err)
	}
	dash, err := web.New(web.Config{Actions: e.svc, Identity: rolesIdentity{e.auth, roles}, Clock: e.clock, LoginProviders: []string{"google", "github"}})
	if err != nil {
		t.Fatal(err)
	}
	rest, err := api.New(api.Config{Actions: e.svc, Identity: e.auth, Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/auth/", e.auth.Handler())
	mux.Handle(api.MountPath, rest)
	mux.Handle("/", dash)
	ts.Config.Handler = mux
	ts.Start()
	t.Cleanup(ts.Close)
	return e
}

// browser is a user agent with a cookie jar that never follows redirects.
type browser struct {
	e *env
	c *http.Client
}

func (e *env) browser(t *testing.T) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{e: e, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type reply struct {
	status int
	header http.Header
	body   string
}

func (b *browser) do(t *testing.T, method, target, body string, header map[string]string) reply {
	t.Helper()
	if strings.HasPrefix(target, "/") {
		target = b.e.base + target
	}
	req, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{status: resp.StatusCode, header: resp.Header, body: string(raw)}
}

func (b *browser) get(t *testing.T, target string) reply {
	return b.do(t, http.MethodGet, target, "", nil)
}

// post sends a form with the session's CSRF token added.
func (b *browser) post(t *testing.T, target string, form url.Values) reply {
	t.Helper()
	f := url.Values{}
	for k, v := range form {
		f[k] = v
	}
	if f.Get("csrf_token") == "" {
		f.Set("csrf_token", b.csrf(t))
	}
	return b.postRaw(t, target, f)
}

func (b *browser) postRaw(t *testing.T, target string, form url.Values) reply {
	t.Helper()
	return b.do(t, http.MethodPost, target, form.Encode(), navigatedForm())
}

// navigatedForm are the headers a browser sends with a form a person submits
// on one of the dashboard's pages.
func navigatedForm() map[string]string {
	return map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Mode": "navigate",
		"Sec-Fetch-Dest": "document", "Sec-Fetch-Site": "same-origin", "Sec-Fetch-User": "?1"}
}

func (b *browser) hop(t *testing.T, method, target string) string {
	t.Helper()
	r := b.do(t, method, target, "", nil)
	if r.status < 300 || r.status > 399 || r.header.Get("Location") == "" {
		t.Fatalf("%s %s: %d, not a redirect: %.200s", method, target, r.status, r.body)
	}
	return r.header.Get("Location")
}

// signIn runs a whole sign-in as subject at the fake provider.
func (b *browser) signIn(t *testing.T, subject string) {
	t.Helper()
	b.signInWith(t, "google", subject)
}

// signInWith signs subject in through provider.
func (b *browser) signInWith(t *testing.T, provider, subject string) {
	t.Helper()
	if !b.e.fake.SignInAs(subject) {
		t.Fatalf("unknown subject %s", subject)
	}
	loc := b.hop(t, http.MethodGet, "/auth/login/"+provider+"?return_to=/")
	b.hop(t, http.MethodGet, b.hop(t, http.MethodGet, loc))
}

// stepUpFor confirms the signed-in person's identity again at the
// provider, for the one call named by action and target.
func (b *browser) stepUpFor(t *testing.T, subject, action, target string) {
	t.Helper()
	b.e.fake.SignInAs(subject)
	r := b.postRaw(t, "/auth/step-up/google", url.Values{"csrf_token": {b.csrf(t)}, "action": {action}, "target": {target}})
	if r.header.Get("Location") == "" {
		t.Fatalf("step-up did not redirect: %d %.200s", r.status, r.body)
	}
	b.hop(t, http.MethodGet, b.hop(t, http.MethodGet, r.header.Get("Location")))
}

var (
	stepUpAction = regexp.MustCompile(`action="/auth/step-up/google[^"]*">.*?name="action" value="([^"]+)">`)
	stepUpTarget = regexp.MustCompile(`action="/auth/step-up/google[^"]*">.*?name="target" value="([^"]+)">`)
)

// stepUpFromPage submits the step-up form a page offers, as the person
// would, and returns the call it confirmed.
func (b *browser) stepUpFromPage(t *testing.T, subject, page string) (action, target string) {
	t.Helper()
	a, g := stepUpAction.FindStringSubmatch(page), stepUpTarget.FindStringSubmatch(page)
	if a == nil || g == nil {
		t.Fatalf("the page offers no step-up for a named call: %.600s", page)
	}
	action, target = html.UnescapeString(a[1]), html.UnescapeString(g[1])
	b.stepUpFor(t, subject, action, target)
	return action, target
}

func (b *browser) csrf(t *testing.T) string {
	t.Helper()
	var view struct {
		CSRFToken string `json:"csrf_token"`
	}
	r := b.get(t, "/auth/session")
	if err := json.Unmarshal([]byte(r.body), &view); err != nil {
		t.Fatalf("session view: %v: %.200s", err, r.body)
	}
	return view.CSRFToken
}

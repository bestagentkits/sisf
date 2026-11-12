package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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

// stepClock is the clock of the CLI under test: a wait ends only when the
// test ticks, and moves the factory's clock forward by the chosen time.
type stepClock struct {
	*intaketest.Clock
	ticks chan time.Duration
}

func (c stepClock) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	go func() { c.Advance(<-c.ticks); ch <- c.Now() }()
	return ch
}

// lockedBuffer collects what the CLI prints and tells the test when the
// verification code has appeared.
type lockedBuffer struct {
	mu   sync.Mutex
	text strings.Builder
	code chan string
}

var userCodePattern = regexp.MustCompile(`code ([A-Z2-9]{4}-[A-Z2-9]{4})`)

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.text.Write(p)
	if m := userCodePattern.FindStringSubmatch(b.text.String()); m != nil {
		select {
		case b.code <- m[1]:
		default:
		}
	}
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

// factory is a real REST surface with real sign-in at a fake provider.
type factory struct {
	url   string
	fake  *fakeoidc.Server
	clock stepClock
	auth  *auth.Service
}

type tokenOnlyAuthorizer struct{}

func (tokenOnlyAuthorizer) Authorize(context.Context, core.AuthzRequest) (core.AuthzDecision, error) {
	return core.AuthzDecision{Effect: core.EffectAllow}, nil
}

type fixedPolicy struct{}

func (fixedPolicy) PolicyRevision(context.Context) (core.CommitSHA, error) {
	return "1111111111111111111111111111111111111111", nil
}

func newFactory(t *testing.T) *factory {
	t.Helper()
	clock := stepClock{Clock: intaketest.NewClock(time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)), ticks: make(chan time.Duration)}
	f := &factory{clock: clock, fake: fakeoidc.NewTestServer(t, fakeoidc.WithClock(clock))}
	ts := httptest.NewUnstartedServer(nil)
	f.url = "http://" + ts.Listener.Addr().String()
	f.fake.AddClient(fakeoidc.Client{ID: "sisf", Secret: "s3", RedirectURIs: []string{f.url + "/auth/callback/google"}})
	f.fake.AddUser(fakeoidc.User{Subject: "owner-sub", Email: "owner@example.test", EmailVerified: true, Login: "owner", GitHubID: 1001})
	google, err := auth.NewOIDCProvider(context.Background(), auth.OIDCConfig{Name: "google", Issuer: f.fake.Issuer(), ClientID: "sisf",
		ClientSecret: core.NewSecretValue([]byte("s3")), RedirectURL: f.url + "/auth/callback/google", Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if f.auth, err = auth.New(auth.Config{Providers: []identity.Provider{google}, Clock: clock}); err != nil {
		t.Fatal(err)
	}
	reg := actions.NewRegistry()
	echo := func(_ context.Context, req core.ActionRequest) (core.ActionResult, error) {
		return actions.JSONResult(map[string]string{"actor": req.Actor.ID, "surface": string(req.Actor.Surface)})
	}
	if err := reg.Register(core.Action{Spec: core.ActionSpec{Name: "echo.read", Summary: "Echo.", Sensitivity: core.SensitivityRead,
		Scopes: []string{"echo:read"}, Roles: []core.Role{core.RoleOwner}}, Handle: echo}); err != nil {
		t.Fatal(err)
	}
	keys, audit := intaketest.NewKeys("k"), &intaketest.AuditLog{}
	store, err := approvals.NewStore(approvals.Config{State: intaketest.NewState(clock), FactoryID: "acme-app", Signer: keys, Verifier: keys, Audit: audit, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := actions.NewService(actions.Config{FactoryID: "acme-app", Registry: reg, Authorizer: tokenOnlyAuthorizer{}, Approvals: store,
		Policy: fixedPolicy{}, Audit: audit, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.New(api.Config{Actions: svc, Identity: f.auth, Clock: clock, LoginProviders: []string{"google"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/auth/", f.auth.Handler())
	mux.Handle(api.MountPath, handler)
	ts.Config.Handler = mux
	ts.Start()
	t.Cleanup(ts.Close)
	return f
}

// approve signs the Owner in a browser, confirms identity for approving
// exactly code and approves it from the verification page's form.
func (f *factory) approve(t *testing.T, code string) int {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Transport: &http.Transport{}, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(method, target, body string, header map[string]string) (*http.Response, []byte) {
		req, err := http.NewRequest(method, target, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		return resp, raw
	}
	follow := func(method, target string, header map[string]string) string {
		resp, _ := send(method, target, "", header)
		return resp.Header.Get("Location")
	}
	f.fake.SignInAs("owner-sub")
	send(http.MethodGet, follow(http.MethodGet, follow(http.MethodGet, f.url+"/auth/login/google", nil), nil), "", nil)
	var view struct {
		CSRFToken string `json:"csrf_token"`
	}
	_, raw := send(http.MethodGet, f.url+"/auth/session", "", nil)
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("sign-in failed: %s", raw)
	}
	csrf := map[string]string{auth.CSRFHeader: view.CSRFToken}
	stepUp := url.Values{auth.StepUpActionField: {auth.DeviceApprovalStepUpAction}, auth.StepUpTargetField: {code}}
	send(http.MethodGet, follow(http.MethodGet, follow(http.MethodPost, f.url+"/auth/step-up/google?"+stepUp.Encode(), csrf), nil), "", nil)
	form := url.Values{"user_code": {code}, auth.CSRFFormField: {view.CSRFToken}}
	resp, _ := send(http.MethodPost, f.url+api.DeviceApprovePath, form.Encode(),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document", "Sec-Fetch-Site": "same-origin", "Sec-Fetch-User": "?1"})
	return resp.StatusCode
}

// useClock makes the CLI wait on the test's ticks for the length of the test.
func useClock(t *testing.T, c core.Clock) {
	t.Helper()
	old := loginClock
	loginClock = c
	t.Cleanup(func() { loginClock = old })
}

// startLogin runs "sisf login" in the background.
func startLogin(t *testing.T, f *factory, extra ...string) (*lockedBuffer, <-chan int, *lockedBuffer) {
	t.Helper()
	out, errOut := &lockedBuffer{code: make(chan string, 1)}, &lockedBuffer{code: make(chan string, 1)}
	done := make(chan int, 1)
	go func() {
		done <- commands.run(context.Background(), &Env{Stdin: strings.NewReader(""), Stdout: out, Stderr: errOut}, append([]string{"login", "--url", f.url}, extra...))
	}()
	return out, done, errOut
}

func TestLoginStoresAShortLivedScopedTokenAfterTheBrowserApproves(t *testing.T) {
	dir := isolate(t)
	f := newFactory(t)
	useClock(t, f.clock)
	out, done, errOut := startLogin(t, f, "--scope", "echo:read", "--name", "my laptop")
	code := <-out.code
	f.clock.ticks <- 5 * time.Second // the first poll finds nobody has approved yet
	if _, err := os.Stat(filepath.Join(dir, credentialsFile)); err == nil {
		t.Fatal("a login was stored before the person approved")
	}
	if status := f.approve(t, code); status != http.StatusOK {
		t.Fatalf("approve: %d", status)
	}
	f.clock.ticks <- 5 * time.Second
	if status := <-done; status != 0 {
		t.Fatalf("login exited %d: %s", status, errOut.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, credentialsFile))
	if err != nil {
		t.Fatal(err)
	}
	var stored credentials
	if err := json.Unmarshal(raw, &stored); err != nil || !strings.HasPrefix(stored.Token, "sisf_pat_") || stored.URL != f.url {
		t.Fatalf("stored login: %s", raw)
	}
	if info, _ := os.Stat(filepath.Join(dir, credentialsFile)); info.Mode().Perm() != 0o600 {
		t.Fatalf("login file mode %v", info.Mode())
	}
	if ttl := stored.ExpiresAt.Sub(f.clock.Now()); ttl <= 0 || ttl > api.DefaultDeviceTokenTTL {
		t.Fatalf("the token is not short-lived: %v", ttl)
	}
	if strings.Contains(out.String()+errOut.String(), stored.Token) {
		t.Fatal("the token was printed")
	}
	if !strings.Contains(out.String(), f.url+api.DeviceVerifyPath+"?user_code="+code) {
		t.Fatalf("verification URL missing: %s", out.String())
	}
	status, result, errText := runCLI(t, "", "call", "echo.read")
	if status != 0 || !strings.Contains(result, `"surface": "api"`) || !strings.Contains(result, "user:") {
		t.Fatalf("the stored login does not work: %d %s %s", status, result, errText)
	}
	if status, _, errText := runCLI(t, "", "call", "echo.nothing"); status == 0 || !strings.Contains(errText, "NOT_FOUND") {
		t.Fatalf("unknown action: %d %s", status, errText)
	}
}

func TestLoginFailsWhenTheCodeExpiresAndStoresNothing(t *testing.T) {
	dir := isolate(t)
	f := newFactory(t)
	useClock(t, f.clock)
	out, done, errOut := startLogin(t, f)
	<-out.code
	f.clock.ticks <- api.DefaultDeviceCodeTTL
	if status := <-done; status != exitFailure || !strings.Contains(errOut.String(), "EXPIRED") {
		t.Fatalf("expired login: %d %s", status, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, credentialsFile)); err == nil {
		t.Fatal("an expired login stored a token")
	}
}

func TestLoginCannotBeApprovedWithAWrongCodeAndRefusesAnUnreachableFactory(t *testing.T) {
	isolate(t)
	f := newFactory(t)
	useClock(t, f.clock)
	out, done, _ := startLogin(t, f)
	<-out.code
	if status := f.approve(t, "ZZZZ-ZZZZ"); status != http.StatusNotFound {
		t.Fatalf("approving a wrong code: %d", status)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var sink lockedBuffer
	sink.code = make(chan string, 1)
	if status := commands.run(ctx, &Env{Stdin: strings.NewReader(""), Stdout: &sink, Stderr: &sink}, []string{"login", "--url", "http://127.0.0.1:1"}); status != exitFailure ||
		!strings.Contains(sink.String(), "UNAVAILABLE") {
		t.Fatalf("unreachable factory: %d %s", status, sink.String())
	}
	f.clock.ticks <- 5 * time.Second
	f.clock.ticks <- api.DefaultDeviceCodeTTL
	if status := <-done; status != exitFailure {
		t.Fatalf("the wrong approval completed the login: %d", status)
	}
}

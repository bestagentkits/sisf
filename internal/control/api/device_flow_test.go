package api_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/control/api"
	"github.com/bestagentkits/sisf/pkg/core"
)

type started struct {
	DeviceCode      string   `json:"device_code"`
	UserCode        string   `json:"user_code"`
	VerificationURI string   `json:"verification_uri"`
	Complete        string   `json:"verification_uri_complete"`
	ExpiresIn       int      `json:"expires_in"`
	Interval        int      `json:"interval"`
	Scopes          []string `json:"scopes"`
}

type polled struct {
	Status    string   `json:"status"`
	Token     string   `json:"token"`
	TokenType string   `json:"token_type"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
}

func (b *browser) start(t *testing.T, body string) (reply, started) {
	t.Helper()
	r := b.do(t, http.MethodPost, api.DeviceStartPath, body, map[string]string{"Content-Type": "application/json"})
	var s started
	if r.status == http.StatusOK {
		r.json(t, &s)
	}
	return r, s
}

func (b *browser) poll(t *testing.T, deviceCode string) (reply, polled) {
	t.Helper()
	r := b.do(t, http.MethodPost, api.DeviceTokenPath, `{"device_code":"`+deviceCode+`"}`, map[string]string{"Content-Type": "application/json"})
	var p polled
	if r.status == http.StatusOK || r.status == http.StatusAccepted {
		r.json(t, &p)
	}
	return r, p
}

// approve posts the verification page's approve form as the person's own
// navigation, carrying the CSRF token of header, if any, in the form.
func (b *browser) approve(t *testing.T, code string, header map[string]string) reply {
	t.Helper()
	form := url.Values{"user_code": {code}}
	if csrf := header[auth.CSRFHeader]; csrf != "" {
		form.Set(auth.CSRFFormField, csrf)
	}
	h := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Mode": "navigate"}
	for k, v := range header {
		if k != auth.CSRFHeader {
			h[k] = v
		}
	}
	return b.do(t, http.MethodPost, api.DeviceApprovePath, form.Encode(), h)
}

// owner is a browser signed in as the Owner.
func (e *env) owner(t *testing.T) (*browser, map[string]string) {
	t.Helper()
	b := e.browser(t)
	return b, b.signIn(t, "owner-sub")
}

func TestALoginRequestIsApprovedInTheBrowserAndTheTokenIsCollectedOnce(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	cli := e.browser(t)
	r, s := cli.start(t, `{"name":"laptop","scopes":["echo:read"]}`)
	if r.status != http.StatusOK || s.DeviceCode == "" || len(s.UserCode) != 9 || s.Interval == 0 || s.ExpiresIn != 600 ||
		s.VerificationURI != e.base+api.DeviceVerifyPath || !strings.HasSuffix(s.Complete, "?user_code="+s.UserCode) || strings.Contains(r.body, "sisf_pat_") {
		t.Fatalf("start: %d %s", r.status, r.body)
	}
	if r, p := cli.poll(t, s.DeviceCode); r.status != http.StatusAccepted || p.Status != "pending" || p.Token != "" {
		t.Fatalf("poll before approval: %d %s", r.status, r.body)
	}
	person, csrf := e.owner(t)
	page := person.do(t, http.MethodGet, api.DeviceVerifyPath+"?user_code="+url.QueryEscape(s.UserCode), "", nil)
	if page.status != http.StatusOK || !strings.Contains(page.body, s.UserCode) || !strings.Contains(page.body, "echo:read") ||
		!strings.Contains(page.body, `name="csrf_token" value="`+csrf["X-CSRF-Token"]+`"`) || !strings.Contains(page.header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("verification page: %d %s", page.status, page.body)
	}
	person.confirmApproval(t, "owner-sub", csrf, s.UserCode)
	if r := person.approve(t, strings.ToLower(s.UserCode), csrf); r.status != http.StatusOK || strings.Contains(r.body, "sisf_pat_") {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	e.clock.Advance(time.Duration(s.Interval) * time.Second)
	r, p := cli.poll(t, s.DeviceCode)
	if r.status != http.StatusOK || p.Status != "approved" || !strings.HasPrefix(p.Token, "sisf_pat_") || p.TokenType != "Bearer" || len(p.Scopes) != 1 || p.Scopes[0] != "echo:read" {
		t.Fatalf("poll after approval: %d %.60s", r.status, r.body)
	}
	if exp, err := time.Parse(time.RFC3339, p.ExpiresAt); err != nil || exp.Sub(e.clock.Now()) > api.DefaultDeviceTokenTTL {
		t.Fatalf("the token is not short-lived: %s", p.ExpiresAt)
	}
	if r := cli.do(t, http.MethodPost, readPath, "", bearer(p.Token)); r.status != http.StatusOK {
		t.Fatalf("the token does not work: %d %s", r.status, r.body)
	}
	if r := cli.do(t, http.MethodPost, writePath, "", bearer(p.Token)); r.status != http.StatusForbidden {
		t.Fatalf("the token is wider than the scopes asked for: %d", r.status)
	}
	e.clock.Advance(time.Minute)
	if r, _ := cli.poll(t, s.DeviceCode); r.status != http.StatusNotFound || r.errorCode(t) != core.CodeNotFound {
		t.Fatalf("the token was handed over twice: %d %s", r.status, r.body)
	}
	if again := person.approve(t, s.UserCode, csrf); again.status != http.StatusNotFound {
		t.Fatalf("a collected code stayed usable: %d %s", again.status, again.body)
	}
}

func TestACodeApprovedByOnePersonIsNotUsableAgain(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	cli := e.browser(t)
	_, s := cli.start(t, `{}`)
	owner, ownerCSRF := e.owner(t)
	owner.confirmApproval(t, "owner-sub", ownerCSRF, s.UserCode)
	if r := owner.approve(t, s.UserCode, ownerCSRF); r.status != http.StatusOK {
		t.Fatalf("first approval: %d %s", r.status, r.body)
	}
	alice := e.browser(t)
	aliceCSRF := alice.signIn(t, "alice-sub")
	alice.confirmApproval(t, "alice-sub", aliceCSRF, s.UserCode)
	owner.confirmApproval(t, "owner-sub", ownerCSRF, s.UserCode)
	for who, b := range map[string]*browser{"another person": alice, "the same person": owner} {
		csrf := aliceCSRF
		if b == owner {
			csrf = ownerCSRF
		}
		if r := b.approve(t, s.UserCode, csrf); r.status != http.StatusConflict {
			t.Errorf("%s approved a used code: %d %s", who, r.status, r.body)
		}
	}
	e.clock.Advance(10 * time.Second)
	if r, p := cli.poll(t, s.DeviceCode); r.status != http.StatusOK || p.Token == "" {
		t.Fatalf("the original approval was lost: %d", r.status)
	}
}

func TestALoginRequestExpires(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	cli := e.browser(t)
	_, s := cli.start(t, `{}`)
	owner, csrf := e.owner(t)
	owner.confirmApproval(t, "owner-sub", csrf, s.UserCode)
	e.clock.Advance(api.DefaultDeviceCodeTTL)
	if r := owner.approve(t, s.UserCode, csrf); r.status != http.StatusGone {
		t.Fatalf("approve after expiry: %d %s", r.status, r.body)
	}
	_, s2 := cli.start(t, `{}`)
	e.clock.Advance(api.DefaultDeviceCodeTTL)
	if r, _ := cli.poll(t, s2.DeviceCode); r.status != http.StatusGone || r.errorCode(t) != core.CodeExpired {
		t.Fatalf("poll after expiry: %d %s", r.status, r.body)
	}
	if page := owner.do(t, http.MethodGet, api.DeviceVerifyPath+"?user_code="+s2.UserCode, "", nil); page.status != http.StatusNotFound {
		t.Fatalf("page for an expired code: %d", page.status)
	}
}

func TestAWrongOrMalformedCodeIsRefusedAndNothingIsIssued(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	cli := e.browser(t)
	_, s := cli.start(t, `{}`)
	owner, csrf := e.owner(t)
	owner.confirmApproval(t, "owner-sub", csrf, s.UserCode)
	for _, code := range []string{"ZZZZ-ZZZZ", "", strings.Repeat("A", 40), "<script>"} {
		if r := owner.approve(t, code, csrf); r.status < 400 || r.status > 499 {
			t.Errorf("code %q: %d %s", code, r.status, r.body)
		}
	}
	e.clock.Advance(10 * time.Second)
	if r, _ := cli.poll(t, s.DeviceCode); r.status != http.StatusAccepted {
		t.Fatalf("a wrong code changed the request: %d", r.status)
	}
	if r, _ := cli.poll(t, "not-a-device-code"); r.status != http.StatusNotFound {
		t.Fatalf("poll with an unknown device code: %d", r.status)
	}
	if r := cli.do(t, http.MethodPost, api.DeviceTokenPath, `{}`, jsonHeader(nil)); r.status != http.StatusBadRequest {
		t.Fatalf("poll without a device code: %d", r.status)
	}
}

func TestPollingTooFastIsRateLimited(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	cli := e.browser(t)
	_, s := cli.start(t, `{}`)
	cli.poll(t, s.DeviceCode)
	r, _ := cli.poll(t, s.DeviceCode)
	if r.status != http.StatusTooManyRequests || r.errorCode(t) != core.CodeRateLimited || r.header.Get("Retry-After") == "" {
		t.Fatalf("fast poll: %d %s", r.status, r.body)
	}
	e.clock.Advance(api.DefaultPollInterval)
	if r, _ := cli.poll(t, s.DeviceCode); r.status != http.StatusAccepted {
		t.Fatalf("poll after the interval: %d", r.status)
	}
}

func TestApprovalNeedsAFreshStepUpAndABrowserSessionWithItsCSRFToken(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	cli := e.browser(t)
	_, s := cli.start(t, `{}`)
	_, other := cli.start(t, `{}`)
	person := e.browser(t)
	csrf := person.signIn(t, "owner-sub")
	if r := person.approve(t, s.UserCode, csrf); r.status != http.StatusForbidden || !strings.Contains(r.body, "confirm your identity") {
		t.Fatalf("approve without step-up: %d %s", r.status, r.body)
	}
	// A confirmation names the one login request it approves.
	person.confirmApproval(t, "owner-sub", csrf, other.UserCode)
	if r := person.approve(t, s.UserCode, csrf); r.status != http.StatusForbidden || !strings.Contains(r.body, "confirm your identity") {
		t.Fatalf("a confirmation for another login request approved this one: %d %s", r.status, r.body)
	}
	if r := person.approve(t, s.UserCode, nil); r.status != http.StatusForbidden || r.errorCode(t) != core.CodePermissionDenied {
		t.Fatalf("approve without CSRF: %d %s", r.status, r.body)
	}
	if r := cli.approve(t, s.UserCode, bearer(e.token(t, "echo:read"))); r.status < 400 || r.status > 499 {
		t.Fatalf("a token approved a login: %d %s", r.status, r.body)
	}
	if r := cli.approve(t, s.UserCode, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("an anonymous approval: %d", r.status)
	}
	e.clock.Advance(10 * time.Second)
	if r, _ := cli.poll(t, s.DeviceCode); r.status != http.StatusAccepted {
		t.Fatalf("a refused approval changed the request: %d", r.status)
	}
	person.confirmApproval(t, "owner-sub", csrf, s.UserCode)
	if r := person.approve(t, s.UserCode, csrf); r.status != http.StatusOK {
		t.Fatalf("approval after step-up: %d %s", r.status, r.body)
	}
}

// Approving spends a step-up, so a script in the person's browser cannot
// approve, with the session cookie and the CSRF token, by a JSON body or a
// fetch; the refusal spends nothing and claims nothing.
func TestALoginRequestIsApprovedOnlyFromTheVerificationPagesForm(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	cli := e.browser(t)
	_, s := cli.start(t, `{}`)
	person, csrf := e.owner(t)
	person.confirmApproval(t, "owner-sub", csrf, s.UserCode)
	if r := person.do(t, http.MethodPost, api.DeviceApprovePath, `{"user_code":"`+s.UserCode+`"}`, jsonHeader(csrf)); r.status != http.StatusForbidden ||
		r.errorCode(t) != core.CodePermissionDenied {
		t.Fatalf("a JSON approval: %d %s", r.status, r.body)
	}
	for _, mode := range []string{"cors", "same-origin", "no-cors"} {
		if r := person.approve(t, s.UserCode, map[string]string{auth.CSRFHeader: csrf[auth.CSRFHeader], "Sec-Fetch-Mode": mode}); r.status != http.StatusForbidden ||
			r.errorCode(t) != core.CodePermissionDenied {
			t.Errorf("a fetch in mode %s approved: %d %s", mode, r.status, r.body)
		}
	}
	e.clock.Advance(10 * time.Second)
	if r, _ := cli.poll(t, s.DeviceCode); r.status != http.StatusAccepted {
		t.Fatalf("a refused approval changed the request: %d", r.status)
	}
	if r := person.approve(t, s.UserCode, csrf); r.status != http.StatusOK {
		t.Fatalf("the person's own form post after refused scripts: %d %s", r.status, r.body)
	}
}

func TestLoginRequestsRefuseUnknownScopesAndHostileNames(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry)
	cli := e.browser(t)
	for _, body := range []string{`{"scopes":["root:all"]}`, `{"name":"<script>alert(1)</script>"}`, `{"extra":1}`, `{"scopes":["*"]}`} {
		if r, _ := cli.start(t, body); r.status != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, r.status, r.body)
		}
	}
	_, s := cli.start(t, `{}`)
	if len(s.Scopes) != 3 {
		t.Fatalf("default scopes should be every action scope, got %v", s.Scopes)
	}
}

// The step-up button on the verification page posts to /auth/step-up/, which
// redirects to the sign-in provider; the policy must let that form lead
// there, exactly as the dashboard's policy does.
func TestTheVerificationPageLetsTheStepUpFormReachTheProvider(t *testing.T) {
	t.Parallel()
	const provider = "https://accounts.example.test"
	e := newEnv(t, sampleRegistry, func(c *api.Config) { c.FormOrigins = []string{provider} })
	cli := e.browser(t)
	_, s := cli.start(t, `{"name":"laptop","scopes":["echo:read"]}`)
	person, _ := e.owner(t)
	page := person.do(t, http.MethodGet, api.DeviceVerifyPath+"?user_code="+url.QueryEscape(s.UserCode), "", nil)
	csp := page.header.Get("Content-Security-Policy")
	if page.status != http.StatusOK || !strings.Contains(csp, "form-action 'self' "+provider+";") ||
		!strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("verification page policy: %q", csp)
	}
}

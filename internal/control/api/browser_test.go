package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/pkg/core"
)

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
	return &browser{e: e, c: &http.Client{Transport: &http.Transport{}, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// reply is a response read in full.
type reply struct {
	status int
	header http.Header
	body   string
}

func (r reply) json(t *testing.T, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.body), into); err != nil {
		t.Fatalf("body is not JSON (%d): %.200s", r.status, r.body)
	}
}

// errorCode returns the typed code of an error body.
func (r reply) errorCode(t *testing.T) core.ErrorCode {
	t.Helper()
	var body struct {
		Error core.Error `json:"error"`
	}
	r.json(t, &body)
	return body.Error.Code
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

// signIn runs a whole sign-in as subject and returns the session's CSRF header.
func (b *browser) signIn(t *testing.T, subject string) map[string]string {
	t.Helper()
	return b.signInWith(t, "google", subject)
}

// signInWith signs subject in through provider and returns the CSRF header.
func (b *browser) signInWith(t *testing.T, provider, subject string) map[string]string {
	t.Helper()
	if !b.e.fake.SignInAs(subject) {
		t.Fatalf("unknown subject %s", subject)
	}
	loc := b.hop(t, http.MethodGet, "/auth/login/"+provider+"?return_to=/", nil)
	b.hop(t, http.MethodGet, b.hop(t, http.MethodGet, loc, nil), nil)
	return map[string]string{auth.CSRFHeader: b.csrf(t)}
}

// confirmApproval confirms the signed-in person's identity again at the
// provider, for approving the login request with userCode, as the
// verification page's step-up form asks.
func (b *browser) confirmApproval(t *testing.T, subject string, csrf map[string]string, userCode string) {
	t.Helper()
	b.e.fake.SignInAs(subject)
	q := url.Values{auth.StepUpActionField: {auth.DeviceApprovalStepUpAction}, auth.StepUpTargetField: {userCode}}
	loc := b.hop(t, http.MethodPost, "/auth/step-up/google?"+q.Encode(), csrf)
	b.hop(t, http.MethodGet, b.hop(t, http.MethodGet, loc, nil), nil)
}

// hop requests target, expects a redirect and returns where it points.
func (b *browser) hop(t *testing.T, method, target string, header map[string]string) string {
	t.Helper()
	r := b.do(t, method, target, "", header)
	loc := r.header.Get("Location")
	if r.status < 300 || r.status > 399 || loc == "" {
		t.Fatalf("%s %s: %d, not a redirect: %.200s", method, target, r.status, r.body)
	}
	return loc
}

func (b *browser) csrf(t *testing.T) string {
	t.Helper()
	var view struct {
		CSRFToken string `json:"csrf_token"`
	}
	b.do(t, http.MethodGet, "/auth/session", "", nil).json(t, &view)
	return view.CSRFToken
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func jsonHeader(extra map[string]string) map[string]string {
	h := map[string]string{"Content-Type": "application/json"}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

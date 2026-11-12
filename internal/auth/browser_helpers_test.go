package auth_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// browser is a user agent with its own cookie jar that never follows
// redirects by itself, so each hop of a sign-in can be inspected.
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

func (b *browser) do(t *testing.T, method, target, body string, header map[string]string) *http.Response {
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
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// hop requests target and returns the response and its Location.
func (b *browser) hop(t *testing.T, method, target string, header map[string]string) (*http.Response, string) {
	t.Helper()
	resp := b.do(t, method, target, "", header)
	return resp, resp.Header.Get("Location")
}

// toProvider starts a sign-in and returns the provider URL it redirects to.
func (b *browser) toProvider(t *testing.T, provider string) string {
	t.Helper()
	resp, loc := b.hop(t, http.MethodGet, "/auth/login/"+provider+"?return_to=/dashboard", nil)
	if resp.StatusCode != http.StatusFound || loc == "" {
		t.Fatalf("login start: %d", resp.StatusCode)
	}
	return loc
}

// atProvider has the fake sign subject in and returns the callback URL.
func (b *browser) atProvider(t *testing.T, subject, providerURL string) string {
	t.Helper()
	if !b.e.fake.SignInAs(subject) {
		t.Fatalf("unknown subject %s", subject)
	}
	resp, loc := b.hop(t, http.MethodGet, providerURL, nil)
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(loc, b.e.base+"/auth/callback/") {
		t.Fatalf("provider: %d %s", resp.StatusCode, loc)
	}
	return loc
}

// signIn runs a whole sign-in and returns the callback response.
func (b *browser) signIn(t *testing.T, provider, subject string) *http.Response {
	t.Helper()
	resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, subject, b.toProvider(t, provider)), nil)
	return resp
}

// session is the browser's view of its session; ok is false when signed out.
func (b *browser) session(t *testing.T) (view struct {
	ActorID   string `json:"actor_id"`
	SessionID string `json:"session_id"`
	CSRFToken string `json:"csrf_token"`
}, ok bool) {
	t.Helper()
	resp := b.do(t, http.MethodGet, "/auth/session", "", nil)
	if resp.StatusCode != http.StatusOK {
		return view, false
	}
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	return view, true
}

// actor is what the protected API sees for this browser's request.
func (b *browser) actor(t *testing.T, method string, header map[string]string) (core.Actor, int) {
	t.Helper()
	resp := b.do(t, method, "/api/whoami", "", header)
	var a core.Actor
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
			t.Fatal(err)
		}
	}
	return a, resp.StatusCode
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func query(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

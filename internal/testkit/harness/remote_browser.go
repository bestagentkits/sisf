package harness

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/auth"
)

// Browser is one person's browser on a running factory's control plane,
// signing in at the fake identity provider of the simulation or
// development profile (`sisf sim oidc`). It is Person for a factory this
// process did not start: the control plane is reached at its external URL
// and the fake is told who signs in through its /_sim endpoints. No
// redirect is followed by itself, so each hop of a sign-in is checked.
type Browser struct {
	t        testing.TB
	base     string
	provider string
	c        *http.Client
	Subject  string
}

// NewBrowser opens a browser on the control plane at base whose sign-ins
// go through the fake identity provider at provider.
func NewBrowser(t testing.TB, base, provider string) *Browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Browser{t: t, base: strings.TrimRight(base, "/"), provider: strings.TrimRight(provider, "/"),
		c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// SignIn signs subject in through the provider kind (wiring.ProviderOIDC or
// wiring.ProviderGitHub).
func (b *Browser) SignIn(kind, subject string) {
	b.t.Helper()
	b.Subject = subject
	b.signInAs()
	resp := b.Do(http.MethodGet, "/auth/login/"+kind+"?return_to=/", nil, nil)
	b.follow(resp, http.StatusFound)
}

// StepUp confirms the person's identity again for minting one personal
// access token.
func (b *Browser) StepUp() {
	b.t.Helper()
	b.signInAs()
	resp := b.Do(http.MethodPost, "/auth/step-up/"+wiring.ProviderOIDC+"?return_to=/", nil, http.Header{auth.CSRFHeader: {b.CSRF()}})
	b.follow(resp, http.StatusSeeOther)
}

// StepUpFor confirms the person's identity again for the one call of action
// on target, as the dashboard's step-up form asks.
func (b *Browser) StepUpFor(action, target string) {
	b.t.Helper()
	b.signInAs()
	q := url.Values{"return_to": {"/"}, auth.StepUpActionField: {action}, auth.StepUpTargetField: {target}}
	resp := b.Do(http.MethodPost, "/auth/step-up/"+wiring.ProviderOIDC+"?"+q.Encode(), nil, http.Header{auth.CSRFHeader: {b.CSRF()}})
	b.follow(resp, http.StatusSeeOther)
}

// signInAs makes the fake identity provider answer as the person.
func (b *Browser) signInAs() {
	b.t.Helper()
	body, _ := json.Marshal(map[string]string{"subject": b.Subject})
	resp := b.Do(http.MethodPost, b.provider+"/_sim/sign-in", bytes.NewReader(body), http.Header{"Content-Type": {"application/json"}})
	if resp.StatusCode != http.StatusNoContent {
		b.t.Fatalf("harness: the identity provider has no %s (%d)", b.Subject, resp.StatusCode)
	}
}

// follow walks the provider's redirect back to the control plane's callback.
func (b *Browser) follow(resp *http.Response, want int) {
	b.t.Helper()
	if resp.StatusCode != want {
		b.t.Fatalf("harness: the sign-in start answered %d, want %d", resp.StatusCode, want)
	}
	resp = b.Do(http.MethodGet, resp.Header.Get("Location"), nil, nil)
	if resp.StatusCode != http.StatusFound {
		b.t.Fatalf("harness: the identity provider answered %d, want a redirect to the callback", resp.StatusCode)
	}
	resp = b.Do(http.MethodGet, resp.Header.Get("Location"), nil, nil)
	if resp.StatusCode != http.StatusSeeOther {
		b.t.Fatalf("harness: the sign-in callback answered %d", resp.StatusCode)
	}
}

// Session returns the value of the person's session cookie.
func (b *Browser) Session() string {
	b.t.Helper()
	u, _ := url.Parse(b.base)
	for _, c := range b.c.Jar.Cookies(u) {
		if c.Name == auth.SessionCookie {
			return c.Value
		}
	}
	b.t.Fatal("harness: the browser holds no session")
	return ""
}

// CSRF returns the CSRF token of the person's session.
func (b *Browser) CSRF() string {
	b.t.Helper()
	var view struct {
		CSRF string `json:"csrf_token"`
	}
	resp := b.Do(http.MethodGet, auth.SessionPath, nil, nil)
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil || view.CSRF == "" {
		b.t.Fatalf("harness: read the session: %d %v", resp.StatusCode, err)
	}
	return view.CSRF
}

// PAT mints a personal access token with scopes after a fresh step-up.
func (b *Browser) PAT(scopes ...string) string {
	b.t.Helper()
	b.StepUp()
	body, _ := json.Marshal(map[string]any{"name": "e2e", "scopes": scopes})
	resp := b.Do(http.MethodPost, auth.TokensPath, bytes.NewReader(body),
		http.Header{auth.CSRFHeader: {b.CSRF()}, "Content-Type": {"application/json"}})
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Token == "" {
		b.t.Fatalf("harness: mint a token: %d %v", resp.StatusCode, err)
	}
	return out.Token
}

// AccessToken runs the OAuth 2.1 authorization code flow with PKCE an MCP
// client runs, the person consenting, and returns the access token for the
// control plane's /mcp.
func (b *Browser) AccessToken(scopes ...string) string {
	t := b.t
	t.Helper()
	redirect := "http://127.0.0.1:9/callback"
	reg, _ := json.Marshal(map[string]any{"redirect_uris": []string{redirect}, "client_name": "e2e client",
		"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code"}, "response_types": []string{"code"}})
	resp := b.Do(http.MethodPost, "/oauth/register", bytes.NewReader(reg), http.Header{"Content-Type": {"application/json"}})
	var client struct {
		ID string `json:"client_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&client); err != nil || client.ID == "" {
		t.Fatalf("harness: register an OAuth client: %d %v", resp.StatusCode, err)
	}
	verifier := randomText(t)
	sum := sha256.Sum256([]byte(verifier))
	resource := b.base + "/mcp"
	q := url.Values{"response_type": {"code"}, "client_id": {client.ID}, "redirect_uri": {redirect}, "state": {"e2e"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"resource": {resource}, "scope": {strings.Join(scopes, " ")}}
	page := b.Do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil, nil)
	raw, _ := io.ReadAll(page.Body)
	m := consentField.FindSubmatch(raw)
	if page.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("harness: expected the consent page, got %d", page.StatusCode)
	}
	form := url.Values{"consent_id": {string(m[1])}, "decision": {"approve"}}
	resp = b.Do(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()),
		http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Origin": {b.base}})
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Query().Get("code") == "" {
		t.Fatalf("harness: the consent did not yield a code: %d", resp.StatusCode)
	}
	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "redirect_uri": {redirect},
		"client_id": {client.ID}, "code_verifier": {verifier}, "resource": {resource}}
	resp = b.Do(http.MethodPost, "/oauth/token", strings.NewReader(exchange.Encode()),
		http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("harness: exchange the code: %d %v", resp.StatusCode, err)
	}
	return tok.AccessToken
}

// Do sends a request from the browser; a target starting with "/" is on the
// control plane.
func (b *Browser) Do(method, target string, body io.Reader, header http.Header) *http.Response {
	b.t.Helper()
	if strings.HasPrefix(target, "/") {
		target = b.base + target
	}
	if body == nil {
		body = http.NoBody
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		b.t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatalf("harness: %s %s: %v", method, target, err)
	}
	b.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

package harness

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/auth"
)

// Base is the URL of the control zone's HTTP server.
func (f *Factory) Base() string { return f.controlSrv.URL }

// Person is one person's browser on the control zone: a cookie jar, and no
// redirect is followed by itself, so each hop of a sign-in is visible.
type Person struct {
	f       *Factory
	c       *http.Client
	Subject string
}

// SignIn signs the person with subject in through provider (wiring's
// ProviderOIDC or ProviderGitHub) at the fake identity provider.
func (f *Factory) SignIn(provider, subject string) *Person {
	f.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		f.t.Fatal(err)
	}
	p := &Person{f: f, Subject: subject, c: &http.Client{Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	resp := p.Do(http.MethodGet, "/auth/login/"+provider+"?return_to=/", nil, nil)
	p.followSignIn(resp, http.StatusFound)
	return p
}

// SignInOwner signs the Owner in and confirms their identity, as the Owner
// does before deciding an approval.
func (f *Factory) SignInOwner() *Person {
	p := f.SignIn(wiring.ProviderOIDC, OwnerSubject)
	p.StepUp()
	return p
}

// StepUp confirms the person's identity again at the OIDC provider.
func (p *Person) StepUp() {
	p.f.t.Helper()
	resp := p.Do(http.MethodPost, "/auth/step-up/"+wiring.ProviderOIDC+"?return_to=/", nil, http.Header{auth.CSRFHeader: {p.CSRF()}})
	p.followSignIn(resp, http.StatusSeeOther)
}

// followSignIn walks the provider's redirect back to the callback.
func (p *Person) followSignIn(resp *http.Response, want int) {
	p.f.t.Helper()
	if resp.StatusCode != want {
		p.f.t.Fatalf("harness: sign-in start answered %d", resp.StatusCode)
	}
	if !p.f.Identity.SignInAs(p.Subject) {
		p.f.t.Fatalf("harness: the identity provider has no %s", p.Subject)
	}
	resp = p.Do(http.MethodGet, resp.Header.Get("Location"), nil, nil)
	resp = p.Do(http.MethodGet, resp.Header.Get("Location"), nil, nil)
	if resp.StatusCode != http.StatusSeeOther {
		p.f.t.Fatalf("harness: the sign-in callback answered %d", resp.StatusCode)
	}
}

// Session returns the value of the person's session cookie.
func (p *Person) Session() string {
	u, _ := url.Parse(p.f.Base())
	for _, c := range p.c.Jar.Cookies(u) {
		if c.Name == auth.SessionCookie {
			return c.Value
		}
	}
	p.f.t.Fatal("harness: the person holds no session")
	return ""
}

// CSRF returns the CSRF token of the person's session.
func (p *Person) CSRF() string {
	p.f.t.Helper()
	var view struct {
		CSRF string `json:"csrf_token"`
	}
	resp := p.Do(http.MethodGet, auth.SessionPath, nil, nil)
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil || view.CSRF == "" {
		p.f.t.Fatalf("harness: read the session: %d %v", resp.StatusCode, err)
	}
	return view.CSRF
}

// PAT mints a personal access token with scopes; the person must have
// stepped up recently.
func (p *Person) PAT(scopes ...string) string {
	p.f.t.Helper()
	body, _ := json.Marshal(map[string]any{"name": "harness", "scopes": scopes})
	resp := p.Do(http.MethodPost, auth.TokensPath, bytes.NewReader(body),
		http.Header{auth.CSRFHeader: {p.CSRF()}, "Content-Type": {"application/json"}})
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Token == "" {
		p.f.t.Fatalf("harness: mint a token: %d %v", resp.StatusCode, err)
	}
	return out.Token
}

// consentField finds the consent form's hidden field.
var consentField = regexp.MustCompile(`name="consent_id" value="([^"]+)"`)

// AccessToken runs the OAuth 2.1 flow an MCP client runs, with the person
// consenting, and returns the access token for the MCP endpoint.
func (p *Person) AccessToken(scopes ...string) string {
	t := p.f.t
	t.Helper()
	redirect := "http://127.0.0.1:9/callback"
	reg, _ := json.Marshal(map[string]any{"redirect_uris": []string{redirect}, "client_name": "harness client",
		"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code"}, "response_types": []string{"code"}})
	resp := p.Do(http.MethodPost, "/oauth/register", bytes.NewReader(reg), http.Header{"Content-Type": {"application/json"}})
	var client struct {
		ID string `json:"client_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&client); err != nil || client.ID == "" {
		t.Fatalf("harness: register an OAuth client: %d %v", resp.StatusCode, err)
	}
	verifier := randomText(t)
	sum := sha256.Sum256([]byte(verifier))
	resource := p.f.Base() + "/mcp"
	q := url.Values{"response_type": {"code"}, "client_id": {client.ID}, "redirect_uri": {redirect}, "state": {"harness"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"resource": {resource}, "scope": {strings.Join(scopes, " ")}}
	page := p.Do(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil, nil)
	raw, _ := io.ReadAll(page.Body)
	m := consentField.FindSubmatch(raw)
	if page.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("harness: expected the consent page, got %d", page.StatusCode)
	}
	form := url.Values{"consent_id": {string(m[1])}, "decision": {"approve"}}
	resp = p.Do(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()),
		http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Origin": {p.f.Base()}})
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Query().Get("code") == "" {
		t.Fatalf("harness: the consent did not yield a code: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "redirect_uri": {redirect},
		"client_id": {client.ID}, "code_verifier": {verifier}, "resource": {resource}}
	resp = p.Do(http.MethodPost, "/oauth/token", strings.NewReader(exchange.Encode()),
		http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("harness: exchange the code: %d %v", resp.StatusCode, err)
	}
	return tok.AccessToken
}

// Do sends a request as the person; a target starting with "/" is on the
// control zone.
func (p *Person) Do(method, target string, body io.Reader, header http.Header) *http.Response {
	p.f.t.Helper()
	if strings.HasPrefix(target, "/") {
		target = p.f.Base() + target
	}
	if body == nil {
		body = http.NoBody
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		p.f.t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := p.c.Do(req)
	if err != nil {
		p.f.t.Fatalf("harness: %s %s: %v", method, target, err)
	}
	p.f.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func randomText(t interface{ Fatal(...any) }) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

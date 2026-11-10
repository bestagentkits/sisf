package oauthastest

import (
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
	"testing"

	"github.com/bestagentkits/sisf/internal/oauthas"
)

// Browser is a person's user agent: its own cookie jar, and it never
// follows a redirect by itself, so each hop can be inspected.
type Browser struct {
	e *Env
	c *http.Client
}

// Browser returns a new browser with no session.
func (e *Env) Browser(t testing.TB) *Browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Browser{e: e, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Do sends a request; a target starting with "/" is on the environment.
// A non-nil form is sent as the urlencoded body.
func (b *Browser) Do(t testing.TB, method, target string, form url.Values, header http.Header) *http.Response {
	t.Helper()
	return do(t, b.c, b.e.Base, method, target, form, header)
}

// SignIn signs the person with subject in through the session flow.
func (b *Browser) SignIn(t testing.TB, subject string) {
	t.Helper()
	resp := b.Do(t, http.MethodGet, LoginPath+"?return_to=/", nil, nil)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("login start: %d", resp.StatusCode)
	}
	if !b.e.Provider.SignInAs(subject) {
		t.Fatalf("unknown subject %s", subject)
	}
	resp = b.Do(t, http.MethodGet, resp.Header.Get("Location"), nil, nil)
	resp = b.Do(t, http.MethodGet, resp.Header.Get("Location"), nil, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in callback: %d", resp.StatusCode)
	}
}

// consentID finds the consent form's hidden field on a consent page.
var consentID = regexp.MustCompile(`name="consent_id" value="([^"]+)"`)

// Decide answers the consent page resp with decision ("approve" or "deny")
// and returns the response, a redirect to the client on success.
func (b *Browser) Decide(t testing.TB, resp *http.Response, decision string) *http.Response {
	t.Helper()
	m := consentID.FindStringSubmatch(readAll(t, resp))
	if resp.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("expected a consent page, got %d", resp.StatusCode)
	}
	return b.Do(t, http.MethodPost, oauthas.AuthorizePath, url.Values{"consent_id": {m[1]}, "decision": {decision}}, nil)
}

// PKCE is a code verifier and its S256 challenge.
type PKCE struct{ Verifier, Challenge string }

// NewPKCE returns a fresh verifier and challenge.
func NewPKCE(t testing.TB) PKCE {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	v := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(v))
	return PKCE{Verifier: v, Challenge: base64.RawURLEncoding.EncodeToString(sum[:])}
}

// AuthorizeQuery is a valid authorization request for ClientID; tests edit
// it to make it wrong in one way.
func (e *Env) AuthorizeQuery(p PKCE, scopes ...string) url.Values {
	q := url.Values{"response_type": {"code"}, "client_id": {ClientID}, "redirect_uri": {e.RedirectURI},
		"state": {"st-123"}, "code_challenge": {p.Challenge}, "code_challenge_method": {"S256"},
		"resource": {e.Base + ResourcePath}}
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	return q
}

// Code returns the code in a redirect to the client, failing the test on
// an error redirect.
func Code(t testing.TB, resp *http.Response) string {
	t.Helper()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || err != nil || loc.Query().Get("code") == "" {
		t.Fatalf("expected a redirect with a code, got %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	return loc.Query().Get("code")
}

// ExchangeForm is a valid token request for code and p.
func (e *Env) ExchangeForm(code string, p PKCE) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {e.RedirectURI},
		"client_id": {ClientID}, "code_verifier": {p.Verifier}, "resource": {e.Base + ResourcePath}}
}

// TokenResponse is the decoded answer of the token endpoint.
type TokenResponse struct {
	Status      int
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
}

// Exchange posts form to the token endpoint.
func (e *Env) Exchange(t testing.TB, form url.Values) TokenResponse {
	t.Helper()
	resp := do(t, e.HTTP, e.Base, http.MethodPost, oauthas.TokenPath, form, nil)
	var out TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("token response: %v", err)
	}
	out.Status = resp.StatusCode
	return out
}

// Token runs the whole flow for subject, who must already hold roles, and
// returns the access token.
func (e *Env) Token(t testing.TB, subject string, scopes ...string) string {
	t.Helper()
	b := e.Browser(t)
	b.SignIn(t, subject)
	p := NewPKCE(t)
	page := b.Do(t, http.MethodGet, oauthas.AuthorizePath+"?"+e.AuthorizeQuery(p, scopes...).Encode(), nil, nil)
	tok := e.Exchange(t, e.ExchangeForm(Code(t, b.Decide(t, page, "approve")), p))
	if tok.Status != http.StatusOK || tok.AccessToken == "" {
		t.Fatalf("token exchange: %d %s", tok.Status, tok.Error)
	}
	return tok.AccessToken
}

func do(t testing.TB, c *http.Client, base, method, target string, form url.Values, header http.Header) *http.Response {
	t.Helper()
	if strings.HasPrefix(target, "/") {
		target = base + target
	}
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func readAll(t testing.TB, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

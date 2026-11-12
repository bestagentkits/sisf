package oauthas_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/bestagentkits/sisf/internal/oauthas"
	"github.com/bestagentkits/sisf/internal/oauthas/oauthastest"
)

func TestScopesBeyondWhatThePersonHoldsAreRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := signedIn(t, e, "viewer-sub")
	p := oauthastest.NewPKCE(t)
	for _, scope := range []string{"policy:write", "runs:read policy:write", "admin", "runs:*"} {
		q := e.AuthorizeQuery(p)
		q.Set("scope", scope)
		if got := redirectError(t, e, authorize(t, e, b, q)); got != "invalid_scope" {
			t.Errorf("a Viewer asking for %q: error = %q", scope, got)
		}
	}
	tok := e.Exchange(t, e.ExchangeForm(codeFor(t, e, "viewer-sub", p), p))
	if tok.Scope != "runs:read" {
		t.Fatalf("a Viewer was granted %q", tok.Scope)
	}
	// A person holding no role has nothing to delegate.
	nobody := signedIn(t, e, "nobody-sub")
	if got := redirectError(t, e, authorize(t, e, nobody, e.AuthorizeQuery(p))); got != "invalid_scope" {
		t.Fatalf("a person with no role: error = %q", got)
	}
}

func TestImplicitGrantAndOtherFlowsAreRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := signedIn(t, e, "viewer-sub")
	p := oauthastest.NewPKCE(t)
	for _, rt := range []string{"token", "code token", "id_token", ""} {
		q := e.AuthorizeQuery(p)
		q.Set("response_type", rt)
		if got := redirectError(t, e, authorize(t, e, b, q)); got != "unsupported_response_type" {
			t.Errorf("response_type %q: error = %q", rt, got)
		}
	}
	for _, grant := range []string{"password", "client_credentials", "refresh_token", "implicit"} {
		form := url.Values{"grant_type": {grant}, "client_id": {oauthastest.ClientID}, "username": {"u"}, "password": {"p"}}
		if tok := e.Exchange(t, form); tok.Error != "unsupported_grant_type" {
			t.Errorf("grant %s: %+v", grant, tok)
		}
	}
}

func TestClientSecretsAndTokensNeverTravelInURLs(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := signedIn(t, e, "viewer-sub")
	p := oauthastest.NewPKCE(t)
	q := e.AuthorizeQuery(p)
	q.Set("client_secret", "s3cret")
	if resp := authorize(t, e, b, q); resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Fatalf("a secret in the authorization URL was accepted: %d", resp.StatusCode)
	}
	code := codeFor(t, e, "viewer-sub", p)
	resp, err := e.HTTP.Post(e.Base+oauthas.TokenPath+"?"+e.ExchangeForm(code, p).Encode(), "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("token parameters in the URL were accepted: %d", resp.StatusCode)
	}
	form := e.ExchangeForm(code, p)
	form.Set("client_secret", "s3cret")
	if tok := e.Exchange(t, form); tok.Error != "invalid_client" {
		t.Fatalf("a client secret was accepted from a public client: %+v", tok)
	}
}

func TestATokenCannotStandInForTheBrowserSession(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := signedIn(t, e, "viewer-sub")
	q := e.AuthorizeQuery(oauthastest.NewPKCE(t))
	resp := b.Do(t, http.MethodGet, oauthas.AuthorizePath+"?"+q.Encode(), nil, http.Header{"Authorization": {"Bearer " + e.Token(t, "viewer-sub")}})
	if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Fatalf("an authorization made with a bearer token: %d", resp.StatusCode)
	}
}

func TestConsentIsSingleUseBoundToItsSessionAndSameSite(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	owner := signedIn(t, e, "owner-sub")
	viewer := signedIn(t, e, "viewer-sub")
	page := authorize(t, e, owner, e.AuthorizeQuery(oauthastest.NewPKCE(t)))
	body := readBody(t, page)
	id := between(body, `name="consent_id" value="`, `"`)
	form := url.Values{"consent_id": {id}, "decision": {"approve"}}
	crossSite := http.Header{"Sec-Fetch-Site": {"cross-site"}, "Origin": {"https://evil.example"}}
	if resp := owner.Do(t, http.MethodPost, oauthas.AuthorizePath, form, crossSite); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a cross-site consent was accepted: %d", resp.StatusCode)
	}
	if resp := viewer.Do(t, http.MethodPost, oauthas.AuthorizePath, form, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("another person answered the Owner's consent: %d", resp.StatusCode)
	}
	if resp := owner.Do(t, http.MethodPost, oauthas.AuthorizePath, form, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a consent ID was usable after another session tried it: %d", resp.StatusCode)
	}
}

func TestDynamicClientsArePublicAndGainNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for name, body := range map[string]string{
		"secret auth":    `{"redirect_uris":["https://app.example/cb"],"token_endpoint_auth_method":"client_secret_basic"}`,
		"implicit":       `{"redirect_uris":["https://app.example/cb"],"response_types":["token"]}`,
		"password grant": `{"redirect_uris":["https://app.example/cb"],"grant_types":["password"]}`,
		"http redirect":  `{"redirect_uris":["http://app.example/cb"]}`,
		"fragment":       `{"redirect_uris":["https://app.example/cb#x"]}`,
		"no redirect":    `{"client_name":"x"}`,
	} {
		if status, _ := register(t, e, body); status != http.StatusBadRequest {
			t.Errorf("%s: registered with %d", name, status)
		}
	}
	status, reg := register(t, e, `{"client_name":"<b>Claude</b>","redirect_uris":["http://127.0.0.1:9/callback"],"grant_types":["authorization_code","refresh_token"]}`)
	if status != http.StatusCreated || reg["client_secret"] != nil || reg["token_endpoint_auth_method"] != "none" {
		t.Fatalf("registration: %d %v", status, reg)
	}
	b := signedIn(t, e, "viewer-sub")
	q := e.AuthorizeQuery(oauthastest.NewPKCE(t))
	q.Set("client_id", reg["client_id"].(string))
	page := authorize(t, e, b, q)
	html := readBody(t, page)
	if !bytes.Contains([]byte(html), []byte("not verified")) || bytes.Contains([]byte(html), []byte("<b>Claude</b>")) {
		t.Fatal("the consent page must flag a self-registered name and escape it")
	}
	q.Del("code_challenge")
	if got := redirectError(t, e, authorize(t, e, b, q)); got != "invalid_request" {
		t.Fatalf("a registered client skipped PKCE: %q", got)
	}
}

func register(t *testing.T, e *oauthastest.Env, body string) (int, map[string]any) {
	t.Helper()
	resp, err := e.HTTP.Post(e.Base+oauthas.RegisterPath, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func between(s, start, end string) string {
	i := bytes.Index([]byte(s), []byte(start))
	if i < 0 {
		return ""
	}
	rest := s[i+len(start):]
	j := bytes.Index([]byte(rest), []byte(end))
	if j < 0 {
		return ""
	}
	return rest[:j]
}

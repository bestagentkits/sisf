package fakeoidc_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/bestagentkits/sisf/internal/testkit/fakeoidc"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

const redirect = "http://127.0.0.1:1/callback"

var noFollow = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func setup(t *testing.T) (*fakeoidc.Server, *sqlstoretest.Clock) {
	t.Helper()
	clock := sqlstoretest.NewClock(time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC))
	fake := fakeoidc.NewTestServer(t, fakeoidc.WithClock(clock))
	fake.AddClient(fakeoidc.Client{ID: "rp", Secret: "rp-secret", RedirectURIs: []string{redirect}})
	fake.AddUser(fakeoidc.User{Subject: "sub-1", Email: "a@example.test", EmailVerified: true, Login: "alice", GitHubID: 42})
	if !fake.SignInAs("sub-1") {
		t.Fatal("SignInAs refused a known user")
	}
	return fake, clock
}

// authorize runs the authorization endpoint and returns the redirect query.
func authorize(t *testing.T, fake *fakeoidc.Server, path string, extra url.Values) url.Values {
	t.Helper()
	q := url.Values{"client_id": {"rp"}, "redirect_uri": {redirect}, "state": {"st"}, "response_type": {"code"}, "nonce": {"n-1"}}
	for k, v := range extra {
		q[k] = v
	}
	resp, err := noFollow.Get(fake.Issuer() + path + "?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || err != nil {
		t.Fatalf("authorize: status %d", resp.StatusCode)
	}
	return loc.Query()
}

func exchange(t *testing.T, fake *fakeoidc.Server, path, code string, extra url.Values) (int, map[string]any) {
	t.Helper()
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}}
	for k, v := range extra {
		form[k] = v
	}
	req, _ := http.NewRequest(http.MethodPost, fake.Issuer()+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("rp", "rp-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func TestOIDCCodeFlowIssuesASignedIDTokenWithTheNonce(t *testing.T) {
	t.Parallel()
	fake, _ := setup(t)
	q := authorize(t, fake, fakeoidc.OIDCAuthorizePath, nil)
	if q.Get("state") != "st" || q.Get("code") == "" {
		t.Fatalf("redirect query = %v", q)
	}
	status, body := exchange(t, fake, fakeoidc.OIDCTokenPath, q.Get("code"), nil)
	if status != http.StatusOK {
		t.Fatalf("token: %d %v", status, body)
	}
	tok, err := jwt.ParseSigned(body["id_token"].(string), []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatal(err)
	}
	var keys jose.JSONWebKeySet
	resp, err := http.Get(fake.Issuer() + fakeoidc.JWKSPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(&keys); err != nil {
		t.Fatal(err)
	}
	var claims struct {
		jwt.Claims
		Nonce string `json:"nonce"`
	}
	if err := tok.Claims(keys.Keys[0].Key, &claims); err != nil {
		t.Fatalf("the ID token does not verify against the JWKS: %v", err)
	}
	if claims.Issuer != fake.Issuer() || claims.Subject != "sub-1" || claims.Nonce != "n-1" || !claims.Audience.Contains("rp") {
		t.Fatalf("claims = %+v", claims)
	}
	if again, _ := exchange(t, fake, fakeoidc.OIDCTokenPath, q.Get("code"), nil); again != http.StatusBadRequest {
		t.Fatalf("a code must be single-use, replay got %d", again)
	}
}

func TestAuthorizationCodesAreBoundToPKCEAndExpire(t *testing.T) {
	t.Parallel()
	fake, clock := setup(t)
	challenge := url.Values{"code_challenge": {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"}, "code_challenge_method": {"S256"}}
	q := authorize(t, fake, fakeoidc.OIDCAuthorizePath, challenge)
	if status, _ := exchange(t, fake, fakeoidc.OIDCTokenPath, q.Get("code"), url.Values{"code_verifier": {"wrong"}}); status != http.StatusBadRequest {
		t.Fatalf("a wrong PKCE verifier was accepted: %d", status)
	}
	q = authorize(t, fake, fakeoidc.OIDCAuthorizePath, challenge)
	if status, _ := exchange(t, fake, fakeoidc.OIDCTokenPath, q.Get("code"),
		url.Values{"code_verifier": {"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"}}); status != http.StatusOK {
		t.Fatalf("the right PKCE verifier was refused: %d", status)
	}
	q = authorize(t, fake, fakeoidc.OIDCAuthorizePath, nil)
	clock.Advance(2 * time.Minute)
	if status, _ := exchange(t, fake, fakeoidc.OIDCTokenPath, q.Get("code"), nil); status != http.StatusBadRequest {
		t.Fatalf("an expired code was accepted: %d", status)
	}
	q = authorize(t, fake, fakeoidc.GitHubAuthorizePath, nil)
	if status, _ := exchange(t, fake, fakeoidc.OIDCTokenPath, q.Get("code"), nil); status != http.StatusBadRequest {
		t.Fatalf("a GitHub-dialect code was redeemed at the OIDC endpoint: %d", status)
	}
}

func TestUnregisteredRedirectIsNeverFollowed(t *testing.T) {
	t.Parallel()
	fake, _ := setup(t)
	resp, err := noFollow.Get(fake.Issuer() + fakeoidc.OIDCAuthorizePath + "?client_id=rp&response_type=code&redirect_uri=" + url.QueryEscape("https://evil.example/cb"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Fatalf("status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestGitHubDialectServesTheUserAndRepositoryPermission(t *testing.T) {
	t.Parallel()
	fake, _ := setup(t)
	fake.SetRepoPermission("acme/app", "alice", "maintain")
	q := authorize(t, fake, fakeoidc.GitHubAuthorizePath, nil)
	status, body := exchange(t, fake, fakeoidc.GitHubTokenPath, q.Get("code"), nil)
	if status != http.StatusOK || body["id_token"] != nil {
		t.Fatalf("token: %d %v", status, body)
	}
	get := func(path, token string) map[string]any {
		req, _ := http.NewRequest(http.MethodGet, fake.Issuer()+fakeoidc.GitHubAPIPrefix+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	if user := get("/user", body["access_token"].(string)); user["login"] != "alice" || user["id"] != float64(42) {
		t.Fatalf("user = %v", user)
	}
	if user := get("/user", "forged"); user["login"] != nil {
		t.Fatal("an unknown access token was accepted")
	}
	if p := get("/repos/acme/app/collaborators/alice/permission", "installation"); p["permission"] != "maintain" {
		t.Fatalf("permission = %v", p)
	}
	if p := get("/repos/acme/other/collaborators/alice/permission", "installation"); p["permission"] != "none" {
		t.Fatalf("permission on another repository = %v", p)
	}
}

func TestControlEndpointsConfigureTheFake(t *testing.T) {
	t.Parallel()
	fake, _ := setup(t)
	post := func(path, body string) int {
		resp, err := http.Post(fake.Issuer()+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	// In order: a user must exist before it can be signed in.
	for _, c := range [][2]string{
		{"/_sim/users", `{"subject":"sub-2","login":"bob"}`}, {"/_sim/sign-in", `{"subject":"sub-2"}`},
		{"/_sim/clients", `{"client_id":"c2","redirect_uris":["http://x/cb"]}`}, {"/_sim/faults", `{"nonce":"x"}`},
		{"/_sim/permissions", `{"repo":"acme/app","login":"bob","permission":"read"}`},
	} {
		if status := post(c[0], c[1]); status != http.StatusNoContent {
			t.Errorf("%s: %d", c[0], status)
		}
	}
	for path, body := range map[string]string{"/_sim/sign-in": `{"subject":"ghost"}`, "/_sim/users": `{}`, "/_sim/faults": `{"bogus":1}`} {
		if status := post(path, body); status < 400 {
			t.Errorf("%s %s accepted with %d", path, body, status)
		}
	}
}

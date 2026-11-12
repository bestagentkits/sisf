package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/fakeoidc"
)

// lockedSimOIDC serves the fake identity provider locked behind secret and
// returns its base URL.
func lockedSimOIDC(t *testing.T, secret string) (string, *bytes.Buffer) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := &bytes.Buffer{}
	done := make(chan error, 1)
	opts := simOIDCOptions{addr: ln.Addr().String(), clientID: "sisf-local", redirects: simOIDCRedirects, signInSecret: secret}
	go func() { done <- serveSimOIDC(ctx, &Env{Stdout: out, Stderr: io.Discard}, ln, opts, "") }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	return "http://" + ln.Addr().String(), out
}

func TestALockedFakeIdentityProviderSignsNoOneInWithoutTheSecret(t *testing.T) {
	t.Parallel()
	secret := "s3cr3t-0123456789abcdefghijkl"
	base, out := lockedSimOIDC(t, secret)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Transport: &http.Transport{}, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path, contentType, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if method == http.MethodPost {
			req.Header.Set("Origin", base)
		}
		resp, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp
	}
	signIn := func() int {
		return do(http.MethodPost, "/_sim/sign-in", "application/json", `{"subject":"owner"}`).StatusCode
	}
	authorize := fakeoidc.OIDCAuthorizePath + "?client_id=sisf-local&response_type=code&redirect_uri=" + url.QueryEscape(simOIDCRedirects[0])

	for what, status := range map[string]int{
		"the OIDC sign-in":      do(http.MethodGet, authorize, "", "").StatusCode,
		"the GitHub sign-in":    do(http.MethodGet, fakeoidc.GitHubAuthorizePath+"?client_id=sisf-local", "", "").StatusCode,
		"choosing who signs in": signIn(),
		"a wrong secret": do(http.MethodPost, simUnlockPath, "application/x-www-form-urlencoded",
			url.Values{simUnlockField: {"guess-0123456789abcdefghijkl"}}.Encode()).StatusCode,
	} {
		if status != http.StatusForbidden {
			t.Errorf("%s without the secret: %d, want 403", what, status)
		}
	}
	if status := do(http.MethodGet, fakeoidc.DiscoveryPath, "", "").StatusCode; status != http.StatusOK {
		t.Errorf("discovery, which the control server reads, answered %d", status)
	}
	if status := do(http.MethodGet, simUnlockPath, "", "").StatusCode; status != http.StatusOK {
		t.Errorf("the unlock form answered %d", status)
	}

	if status := do(http.MethodPost, simUnlockPath, "application/x-www-form-urlencoded", url.Values{simUnlockField: {secret}}.Encode()).StatusCode; status != http.StatusOK {
		t.Fatalf("unlocking with the secret: %d", status)
	}
	if status := signIn(); status != http.StatusNoContent {
		t.Errorf("choosing who signs in after unlocking: %d, want 204", status)
	}
	if status := do(http.MethodGet, authorize, "", "").StatusCode; status == http.StatusForbidden {
		t.Error("the unlocked browser is still refused sign-in")
	}
	if strings.Contains(out.String(), secret) {
		t.Error("the secret was written to the provider's output")
	}
}

func TestTheSignInSecretFileMustHoldAUsableSecret(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, content := range map[string]string{"short": "abc\n", "a cookie separator": "0123456789abcdefghijkl;mnop"} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSignInSecret(path); err == nil {
			t.Errorf("a %s secret was accepted", name)
		}
	}
	if _, err := readSignInSecret(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing file was accepted")
	}
	path := filepath.Join(dir, "good")
	if err := os.WriteFile(path, []byte("0123456789abcdefghijklmnopq\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readSignInSecret(path); err != nil || got != "0123456789abcdefghijklmnopq" {
		t.Fatalf("a good secret: %q %v", got, err)
	}
}

// lockedBrowser is a cookie-keeping client of a locked fake that sends
// requests with the headers it is given.
type lockedBrowser struct {
	t    *testing.T
	base string
	c    *http.Client
}

func newLockedBrowser(t *testing.T, base string) *lockedBrowser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &lockedBrowser{t: t, base: base, c: &http.Client{Transport: &http.Transport{}, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *lockedBrowser) post(path, contentType, body string, header map[string]string) *http.Response {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodPost, b.base+path, strings.NewReader(body))
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp
}

func (b *lockedBrowser) unlock(secret string, header map[string]string) *http.Response {
	b.t.Helper()
	return b.post(simUnlockPath, "application/x-www-form-urlencoded", url.Values{simUnlockField: {secret}}.Encode(), header)
}

func (b *lockedBrowser) signIn(header map[string]string) int {
	b.t.Helper()
	return b.post("/_sim/sign-in", "application/json", `{"subject":"owner"}`, header).StatusCode
}

// The unlocked browser keeps a proof of the secret, not the secret: the
// cookie, which goes to every loopback port, never carries it, cannot be
// read by a page's script, and is not sent from another site.
func TestTheUnlockCookieCarriesAProofOfTheSecretNotTheSecret(t *testing.T) {
	t.Parallel()
	secret := "s3cr3t-0123456789abcdefghijkl"
	base, _ := lockedSimOIDC(t, secret)
	b := newLockedBrowser(t, base)
	resp := b.unlock(secret, map[string]string{"Origin": base})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unlocking with the secret: %d", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == simUnlockCookie {
			cookie = c
		}
	}
	switch {
	case cookie == nil:
		t.Fatal("no unlock cookie was set")
	case strings.Contains(cookie.Value, secret) || cookie.Value == "":
		t.Fatalf("the cookie carries the secret: %q", cookie.Value)
	case !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode:
		t.Fatalf("the cookie is not HttpOnly and SameSite=Strict: %+v", cookie)
	}
	if status := b.signIn(map[string]string{"Origin": base}); status != http.StatusNoContent {
		t.Fatalf("the unlocked browser choosing who signs in: %d", status)
	}

	// A cookie that holds the raw secret, the old form, unlocks nothing.
	raw := newLockedBrowser(t, base)
	u, _ := url.Parse(base)
	raw.c.Jar.SetCookies(u, []*http.Cookie{{Name: simUnlockCookie, Value: secret, Path: "/"}})
	if status := raw.signIn(map[string]string{"Origin": base}); status != http.StatusForbidden {
		t.Fatalf("a cookie holding the secret itself unlocked sign-in: %d", status)
	}
}

// Only the fake's own origin may post to /_sim: a page another loopback port
// serves is same-site with the fake, so the cookie alone does not tell it
// from the operator's own use.
func TestALockedFakeTakesPostsToSimOnlyFromItsOwnOrigin(t *testing.T) {
	t.Parallel()
	secret := "s3cr3t-0123456789abcdefghijkl"
	base, _ := lockedSimOIDC(t, secret)
	u, _ := url.Parse(base)
	_, port, _ := net.SplitHostPort(u.Host)
	foreign := map[string]map[string]string{
		"no Origin":                      {},
		"another loopback port's origin": {"Origin": "http://127.0.0.1:9"},
		"a null origin":                  {"Origin": "null"},
		"another site":                   {"Origin": "https://evil.example"},
		"its origin but another Host":    {"Origin": base, "Host": "evil.example:" + port},
	}
	for what, header := range foreign {
		b := newLockedBrowser(t, base)
		if status := b.unlock(secret, header).StatusCode; status != http.StatusForbidden {
			t.Errorf("unlocking from %s: %d, want 403", what, status)
		}
	}
	b := newLockedBrowser(t, base)
	if status := b.unlock(secret, map[string]string{"Origin": base}).StatusCode; status != http.StatusOK {
		t.Fatalf("unlocking from the fake's own origin: %d", status)
	}
	for what, header := range foreign {
		if status := b.signIn(header); status != http.StatusForbidden {
			t.Errorf("the unlocked browser choosing who signs in from %s: %d, want 403", what, status)
		}
	}
	localhost := "http://localhost:" + port
	if status := b.signIn(map[string]string{"Origin": localhost, "Host": "localhost:" + port}); status == http.StatusForbidden {
		t.Errorf("the fake's own loopback listener reached as localhost was refused")
	}
}

func TestTheFakesOwnOriginsAreTheIssuersAndItsListeners(t *testing.T) {
	t.Parallel()
	loopback := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7468}
	got := ownOrigins("http://127.0.0.1:7468", loopback)
	for _, want := range []string{"http://127.0.0.1:7468", "http://localhost:7468"} {
		if !slices.Contains(got, want) {
			t.Errorf("a loopback fake: %v lacks %s", got, want)
		}
	}
	if slices.Contains(got, "http://127.0.0.1:7463") {
		t.Errorf("a loopback fake trusts another port: %v", got)
	}
	got = ownOrigins("http://sim-oidc:7468", &net.TCPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 7468})
	if !slices.Equal(got, []string{"http://sim-oidc:7468", "http://10.0.0.5:7468"}) {
		t.Errorf("a fake behind a service name: %v", got)
	}
}

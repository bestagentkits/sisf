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
	browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path, contentType, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
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

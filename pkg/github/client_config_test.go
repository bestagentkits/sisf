package github_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

func TestClientRefusesUnsafeConfiguration(t *testing.T) {
	t.Parallel()
	token := github.StaticToken(core.NewSecretValue([]byte(testToken)))
	cases := map[string]github.ClientConfig{
		"no token source":          {BaseURL: "https://api.github.com"},
		"cleartext remote":         {BaseURL: "http://api.example.com", Token: token},
		"unparseable":              {BaseURL: "://nope", Token: token},
		"no host":                  {BaseURL: "https://", Token: token},
		"unsupported scheme":       {BaseURL: "ftp://127.0.0.1", Token: token},
		"cleartext lookalike host": {BaseURL: "http://localhost.evil.example", Token: token},
	}
	for name, cfg := range cases {
		if _, err := github.NewRESTClient(cfg); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: err = %v, want INVALID_ARGUMENT", name, err)
		}
	}
	for _, ok := range []string{"", "https://ghe.example.com/api/v3", "http://127.0.0.1:7467", "http://localhost:7467"} {
		if _, err := github.NewRESTClient(github.ClientConfig{BaseURL: ok, Token: token}); err != nil {
			t.Errorf("base %q rejected: %v", ok, err)
		}
	}
}

func TestTokenIsSentAsBearerAndNeverAppearsInErrors(t *testing.T) {
	t.Parallel()
	var seen atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c, _ := github.NewRESTClient(github.ClientConfig{BaseURL: srv.URL, Token: github.StaticToken(core.NewSecretValue([]byte(testToken)))})
	_, err := c.GetIssue(bg, repoRef.Issue(1))
	if seen.Load() != "Bearer "+testToken {
		t.Fatalf("authorization header = %v", seen.Load())
	}
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("error %v must exist and not contain the token", err)
	}
}

func TestMissingOrFailingTokenIsUnauthenticatedAndNothingIsSent(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	sources := map[string]github.TokenSource{
		"empty token":  github.StaticToken(core.SecretValue{}),
		"source fails": func(context.Context) (core.SecretValue, error) { return core.SecretValue{}, errors.New("vault sealed") },
	}
	for name, src := range sources {
		c, _ := github.NewRESTClient(github.ClientConfig{BaseURL: srv.URL, Token: src})
		if _, err := c.GetIssue(bg, repoRef.Issue(1)); !core.HasCode(err, core.CodeUnauthenticated) {
			t.Errorf("%s: %v, want UNAUTHENTICATED", name, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("a request was sent without a token")
	}
}

func TestTokenIsRefusedAndRevokedTokenSurfacesAsUnauthenticated(t *testing.T) {
	t.Parallel()
	f := newFactory(t, func(c *github.ClientConfig) {
		c.Token = github.StaticToken(core.NewSecretValue([]byte("revoked-token")))
	})
	if _, err := f.client.GetIssue(bg, repoRef.Issue(1)); !core.HasCode(err, core.CodeUnauthenticated) {
		t.Fatalf("a refused token: %v, want UNAUTHENTICATED", err)
	}
	if n := len(f.clock.recorded()); n != 0 {
		t.Fatalf("a refused token was retried after waiting %d times", n)
	}
}

func TestRedirectsAreNotFollowedSoTheTokenCannotLeave(t *testing.T) {
	t.Parallel()
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/stolen", http.StatusMovedPermanently)
	}))
	defer srv.Close()
	c, _ := github.NewRESTClient(github.ClientConfig{BaseURL: srv.URL, Token: github.StaticToken(core.NewSecretValue([]byte(testToken)))})
	if _, err := c.GetIssue(bg, repoRef.Issue(1)); err == nil {
		t.Fatal("a redirect must not count as success")
	}
	if elsewhere.Load() != 0 {
		t.Fatal("the client followed a redirect to another host")
	}
}

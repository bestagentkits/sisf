package oauthas

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/pkg/core"
)

// liveSessions treats every session as live and never authenticates a
// browser; it is enough for tests that only mint and verify tokens.
type liveSessions struct{}

func (liveSessions) Authenticate(*http.Request) (core.Actor, error) {
	return core.Actor{}, core.NewError(core.CodeUnauthenticated, "no browser here")
}
func (liveSessions) Active(context.Context, string) bool { return true }

func testServer(t *testing.T, edit func(*Config)) (*Server, error) {
	t.Helper()
	cfg := Config{Issuer: "https://factory.example", Resource: "https://factory.example/mcp", LoginPath: "/login",
		Sessions: liveSessions{}, Scopes: ScopePolicyFunc(nil), Clock: intaketest.NewClock(time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC))}
	if edit != nil {
		edit(&cfg)
	}
	return New(cfg)
}

func TestConfigurationRefusesUnsafeEndpoints(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]func(*Config){
		"http issuer off loopback": func(c *Config) { c.Issuer = "http://factory.example" },
		"issuer with a path":       func(c *Config) { c.Issuer = "https://factory.example/as" },
		"resource with a query":    func(c *Config) { c.Resource = "https://factory.example/mcp?x=1" },
		"resource with fragment":   func(c *Config) { c.Resource = "https://factory.example/mcp#x" },
		"resource trailing slash":  func(c *Config) { c.Resource = "https://factory.example/mcp/" },
		"external login page":      func(c *Config) { c.LoginPath = "https://evil.example/login" },
		"long-lived tokens":        func(c *Config) { c.AccessTokenTTL = 2 * time.Hour },
		"long-lived codes":         func(c *Config) { c.CodeTTL = time.Hour },
		"no session source":        func(c *Config) { c.Sessions = nil },
		"client with http redirect": func(c *Config) {
			c.Clients = []Client{{ID: "c", RedirectURIs: []string{"http://app.example/cb"}}}
		},
	} {
		if _, err := testServer(t, edit); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	if _, err := testServer(t, func(c *Config) { c.Issuer, c.Resource = "http://127.0.0.1:7463", "http://127.0.0.1:7463/mcp" }); err != nil {
		t.Fatalf("a loopback http origin is for local use and must be accepted: %v", err)
	}
}

func TestOnlyAccessTokenTypedJWTsAreAccepted(t *testing.T) {
	t.Parallel()
	s, err := testServer(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := s.cfg.Clock.Now()
	claims := accessClaims{Claims: jwt.Claims{Issuer: s.cfg.Issuer, Subject: "user:x", Audience: jwt.Audience{s.cfg.Resource},
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Minute)), ID: "t1"},
		Scope: "runs:read", SessionID: "ses_1"}
	sign := func(typ string) string {
		opts := &jose.SignerOptions{}
		if typ != "" {
			opts = opts.WithType(jose.ContentType(typ))
		}
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: s.cfg.SigningKey}, opts)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := jwt.Signed(signer).Claims(claims).Serialize()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if _, err := s.VerifyAccessToken(t.Context(), sign(accessTokenType)); err != nil {
		t.Fatalf("a well-formed access token was refused: %v", err)
	}
	for _, typ := range []string{"", "JWT", "id_token+jwt"} {
		if _, err := s.VerifyAccessToken(t.Context(), sign(typ)); !core.HasCode(err, core.CodeUnauthenticated) {
			t.Errorf("a JWT typed %q passed as an access token: %v", typ, err)
		}
	}
}

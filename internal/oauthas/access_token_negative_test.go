package oauthas_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/oauthas"
	"github.com/bestagentkits/sisf/internal/oauthas/oauthastest"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestWrongResourceIsRefusedAtAuthorizeAndToken(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := signedIn(t, e, "viewer-sub")
	p := oauthastest.NewPKCE(t)
	for _, resource := range []string{"", "https://other.example/mcp", e.Base + "/mcp/", e.Base} {
		q := e.AuthorizeQuery(p)
		q.Set("resource", resource)
		if got := redirectError(t, e, authorize(t, e, b, q)); got != "invalid_target" {
			t.Errorf("resource %q: error = %q", resource, got)
		}
	}
	form := e.ExchangeForm(codeFor(t, e, "viewer-sub", p), p)
	form.Set("resource", "https://other.example/mcp")
	if tok := e.Exchange(t, form); tok.Error != "invalid_target" || tok.AccessToken != "" {
		t.Fatalf("a token for another resource was issued: %+v", tok)
	}
}

func TestTokenForAnotherResourceOrIssuerIsRefused(t *testing.T) {
	t.Parallel()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	withKey := func(cfg *oauthas.Config) { cfg.SigningKey = key }
	e := newEnv(t, withKey)
	tok := e.Token(t, "viewer-sub")
	// Servers that share the signing key but protect another resource, or
	// speak as another issuer, must still refuse the token.
	for name, cfg := range map[string]oauthas.Config{
		"another resource": {Issuer: e.Base, Resource: e.Base + "/other"},
		"another issuer":   {Issuer: "https://issuer.example", Resource: e.Base + oauthastest.ResourcePath},
	} {
		cfg.LoginPath, cfg.Sessions, cfg.Clock, cfg.SigningKey = "/login", e.Auth, e.Clock, key
		cfg.Scopes = oauthas.ScopePolicyFunc(nil)
		other, err := oauthas.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := other.VerifyAccessToken(t.Context(), tok); !core.HasCode(err, core.CodeUnauthenticated) {
			t.Errorf("%s accepted the token: %v", name, err)
		}
	}
	if _, err := verify(e, tok); err != nil {
		t.Fatalf("the token must still work where it belongs: %v", err)
	}
}

func TestExpiredAccessTokenIsRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tok := e.Token(t, "viewer-sub")
	e.Clock.Advance(oauthas.DefaultAccessTokenTTL - 1)
	if _, err := verify(e, tok); err != nil {
		t.Fatalf("a live token was refused: %v", err)
	}
	e.Clock.Advance(1)
	if _, err := verify(e, tok); !core.HasCode(err, core.CodeUnauthenticated) {
		t.Fatalf("an expired token was accepted: %v", err)
	}
}

func TestEndedSessionEndsTheTokenBehindIt(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tok := e.Token(t, "viewer-sub")
	actor, err := verify(e, tok)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Auth.RevokeSession(actor.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := verify(e, tok); !core.HasCode(err, core.CodeUnauthenticated) {
		t.Fatalf("a token outlived its revoked session: %v", err)
	}
}

func TestForgedOrMangledTokensAreRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	other := newEnv(t)
	tok := e.Token(t, "viewer-sub")
	parts := strings.Split(tok, ".")
	for name, raw := range map[string]string{
		"signed by another server": other.Token(t, "viewer-sub"),
		"payload swapped":          parts[0] + "." + strings.Split(other.Token(t, "owner-sub"), ".")[1] + "." + parts[2],
		"unsigned":                 parts[0] + "." + parts[1] + ".",
		"not a JWT":                "sisf_pat_abc",
		"empty":                    "",
		"oversized":                strings.Repeat("a", 9000),
	} {
		if _, err := verify(e, raw); !core.HasCode(err, core.CodeUnauthenticated) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	if _, err := verify(e, "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0."); err == nil {
		t.Error("an alg=none token was accepted")
	}
}

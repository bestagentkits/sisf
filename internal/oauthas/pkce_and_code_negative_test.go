package oauthas_test

import (
	"net/http"
	"testing"

	"github.com/bestagentkits/sisf/internal/oauthas"
	"github.com/bestagentkits/sisf/internal/oauthas/oauthastest"
)

// A failed redemption must not consume the code: whoever learns a code
// without its verifier could otherwise burn the legitimate client's sign-in.
// Guessing a verifier is not practical, since it carries at least 256 bits.
func TestWrongPKCEVerifierGetsNoTokenAndLeavesTheCodeForItsClient(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	p := oauthastest.NewPKCE(t)
	code := codeFor(t, e, "viewer-sub", p)
	wrong := e.ExchangeForm(code, oauthastest.NewPKCE(t))
	if tok := e.Exchange(t, wrong); tok.Status != http.StatusBadRequest || tok.Error != "invalid_grant" || tok.AccessToken != "" {
		t.Fatalf("a wrong verifier was accepted: %+v", tok)
	}
	missing := e.ExchangeForm(code, p)
	missing.Del("code_verifier")
	if tok := e.Exchange(t, missing); tok.AccessToken != "" {
		t.Fatal("a missing verifier was accepted")
	}
	tok := e.Exchange(t, e.ExchangeForm(code, p))
	if tok.Status != http.StatusOK || tok.AccessToken == "" {
		t.Fatalf("a failed redemption spent the code: %+v", tok)
	}
	if _, err := verify(e, tok.AccessToken); err != nil {
		t.Fatalf("the token redeemed after failed attempts must work: %v", err)
	}
	if again := e.Exchange(t, e.ExchangeForm(code, p)); again.Error != "invalid_grant" {
		t.Fatalf("a redeemed code was accepted again: %+v", again)
	}
	if _, err := verify(e, tok.AccessToken); err == nil {
		t.Fatal("reusing a redeemed code left its token alive")
	}
}

func TestPlainOrMissingPKCEIsRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := signedIn(t, e, "viewer-sub")
	p := oauthastest.NewPKCE(t)
	plain := e.AuthorizeQuery(p)
	plain.Set("code_challenge_method", "plain")
	plain.Set("code_challenge", p.Verifier)
	none := e.AuthorizeQuery(p)
	none.Del("code_challenge")
	none.Del("code_challenge_method")
	for name, q := range map[string]map[string][]string{"plain": plain, "absent": none} {
		if got := redirectError(t, e, authorize(t, e, b, q)); got != "invalid_request" {
			t.Errorf("%s PKCE: error = %q", name, got)
		}
	}
}

func TestReusedCodeIsRefusedAndRevokesTheTokenItProduced(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	p := oauthastest.NewPKCE(t)
	code := codeFor(t, e, "viewer-sub", p)
	first := e.Exchange(t, e.ExchangeForm(code, p))
	if first.Status != http.StatusOK {
		t.Fatalf("first use: %+v", first)
	}
	if _, err := verify(e, first.AccessToken); err != nil {
		t.Fatalf("the first token must work before the replay: %v", err)
	}
	if again := e.Exchange(t, e.ExchangeForm(code, p)); again.Status != http.StatusBadRequest || again.Error != "invalid_grant" {
		t.Fatalf("a used code was accepted again: %+v", again)
	}
	if _, err := verify(e, first.AccessToken); err == nil {
		t.Fatal("a replayed code left its token alive")
	}
}

func TestExpiredCodeIsRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	p := oauthastest.NewPKCE(t)
	code := codeFor(t, e, "viewer-sub", p)
	e.Clock.Advance(oauthas.DefaultCodeTTL)
	if tok := e.Exchange(t, e.ExchangeForm(code, p)); tok.Error != "invalid_grant" {
		t.Fatalf("an expired code was accepted: %+v", tok)
	}
}

func TestWrongRedirectURIIsRefusedAtBothEndpoints(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := signedIn(t, e, "viewer-sub")
	p := oauthastest.NewPKCE(t)
	for _, uri := range []string{e.RedirectURI + "/", e.RedirectURI + "?x=1", "http://127.0.0.1:9/CALLBACK", "https://evil.example/callback", ""} {
		q := e.AuthorizeQuery(p)
		q.Set("redirect_uri", uri)
		resp := authorize(t, e, b, q)
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
			t.Errorf("redirect URI %q: %d to %q; an unverified URI must never be redirected to", uri, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	code := codeFor(t, e, "viewer-sub", p)
	form := e.ExchangeForm(code, p)
	form.Set("redirect_uri", e.RedirectURI+"/other")
	if tok := e.Exchange(t, form); tok.Error != "invalid_grant" {
		t.Fatalf("a token request with another redirect URI was accepted: %+v", tok)
	}
}

func TestCodeIsBoundToTheClientItWasIssuedTo(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(cfg *oauthas.Config) {
		cfg.Clients = append(cfg.Clients, oauthas.Client{ID: "other-client", RedirectURIs: []string{"http://127.0.0.1:9/callback"}})
	})
	p := oauthastest.NewPKCE(t)
	form := e.ExchangeForm(codeFor(t, e, "viewer-sub", p), p)
	form.Set("client_id", "other-client")
	if tok := e.Exchange(t, form); tok.Error != "invalid_grant" {
		t.Fatalf("another client redeemed the code: %+v", tok)
	}
	form.Set("client_id", oauthastest.ClientID)
	if tok := e.Exchange(t, form); tok.Status != http.StatusOK || tok.AccessToken == "" {
		t.Fatalf("another client's failed attempt spent the code: %+v", tok)
	}
}

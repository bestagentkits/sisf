package auth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
)

// OIDCConfig configures a Google-style OpenID Connect login provider.
type OIDCConfig struct {
	// Name is the provider name used in login paths, for example "google".
	Name string
	// Issuer is the exact issuer URL; discovery must report the same value.
	Issuer       string
	ClientID     string
	ClientSecret core.SecretValue
	// RedirectURL is the callback registered with the provider.
	RedirectURL string
	// Scopes default to openid, email and profile.
	Scopes []string
	// HTTPClient is used for discovery, keys and token calls; nil means the
	// default client.
	HTTPClient *http.Client
	Clock      core.Clock
}

// OIDCProvider signs people in with OpenID Connect. It implements
// identity.Provider and FreshSignInProvider.
type OIDCProvider struct {
	cfg      OIDCConfig
	endpoint oauth2.Endpoint
	verifier *oidc.IDTokenVerifier
}

// FreshSignInProvider is a login provider that can force a new sign-in and
// prove when it happened, which makes step-up a real reauthentication rather
// than a reuse of the provider's own session.
type FreshSignInProvider interface {
	// StepUpURL is AuthorizeURL with the provider told to sign the person in again.
	StepUpURL(req identity.AuthRequest) (string, error)
	// ExchangeFresh is Exchange that also requires the sign-in to have
	// happened at or after notBefore.
	ExchangeFresh(ctx context.Context, ex identity.Exchange, notBefore time.Time) (identity.Identity, error)
}

// NewOIDCProvider discovers the provider at cfg.Issuer. Discovery fails when
// the provider reports a different issuer.
func NewOIDCProvider(ctx context.Context, cfg OIDCConfig) (*OIDCProvider, error) {
	switch {
	case cfg.Name == "" || cfg.ClientID == "" || cfg.RedirectURL == "" || cfg.Clock == nil:
		return nil, invalidArgument("an OIDC provider needs a name, client ID, redirect URL and clock")
	case (identity.Identity{Issuer: cfg.Issuer, Subject: "x"}).Validate() != nil:
		return nil, invalidArgument("an OIDC provider needs an absolute issuer URL")
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}
	p, err := oidc.NewProvider(cfg.clientContext(ctx), cfg.Issuer)
	if err != nil {
		return nil, core.NewError(core.CodeUnavailable, "the OIDC provider "+cfg.Name+" could not be discovered").WithCause(err)
	}
	verifier := p.VerifierContext(cfg.clientContext(context.WithoutCancel(ctx)), &oidc.Config{
		ClientID: cfg.ClientID, Now: cfg.Clock.Now, SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256},
	})
	return &OIDCProvider{cfg: cfg, endpoint: p.Endpoint(), verifier: verifier}, nil
}

func (c OIDCConfig) clientContext(ctx context.Context) context.Context {
	if c.HTTPClient != nil {
		return oidc.ClientContext(ctx, c.HTTPClient)
	}
	return ctx
}

// Name is the configured provider name.
func (p *OIDCProvider) Name() string { return p.cfg.Name }

// Issuer is the issuer every identity from this provider carries.
func (p *OIDCProvider) Issuer() string { return p.cfg.Issuer }

func (p *OIDCProvider) oauth(redirect string) *oauth2.Config {
	if redirect == "" {
		redirect = p.cfg.RedirectURL
	}
	return &oauth2.Config{ClientID: p.cfg.ClientID, ClientSecret: string(p.cfg.ClientSecret.Reveal()),
		Endpoint: p.endpoint, RedirectURL: redirect, Scopes: p.cfg.Scopes}
}

// AuthorizeURL returns where to send the browser to sign in.
func (p *OIDCProvider) AuthorizeURL(req identity.AuthRequest) (string, error) {
	return authorizeURL(p.oauth(req.RedirectURI), req, oidc.Nonce(req.Nonce))
}

// StepUpURL asks the provider for a fresh sign-in, whatever session it has.
func (p *OIDCProvider) StepUpURL(req identity.AuthRequest) (string, error) {
	return authorizeURL(p.oauth(req.RedirectURI), req, oidc.Nonce(req.Nonce),
		oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0"))
}

// Exchange redeems the code and validates the ID token in full.
func (p *OIDCProvider) Exchange(ctx context.Context, ex identity.Exchange) (identity.Identity, error) {
	id, _, err := p.exchange(ctx, ex)
	return id, err
}

// ExchangeFresh is Exchange that also requires auth_time at or after notBefore.
func (p *OIDCProvider) ExchangeFresh(ctx context.Context, ex identity.Exchange, notBefore time.Time) (identity.Identity, error) {
	id, authTime, err := p.exchange(ctx, ex)
	if err != nil {
		return identity.Identity{}, err
	}
	if authTime.IsZero() || authTime.Before(notBefore.Truncate(time.Second)) {
		return identity.Identity{}, unauthenticated("the provider did not sign the person in again")
	}
	return id, nil
}

func (p *OIDCProvider) exchange(ctx context.Context, ex identity.Exchange) (identity.Identity, time.Time, error) {
	if ex.Code == "" || ex.Nonce == "" {
		return identity.Identity{}, time.Time{}, unauthenticated("a sign-in needs a code and a nonce")
	}
	ctx = p.cfg.clientContext(ctx)
	tok, err := exchangeCode(ctx, p.oauth(ex.RedirectURI), ex)
	if err != nil {
		return identity.Identity{}, time.Time{}, err
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return identity.Identity{}, time.Time{}, unauthenticated("the provider returned no ID token")
	}
	idt, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return identity.Identity{}, time.Time{}, unauthenticated("the ID token failed validation (signature, issuer, audience or expiry)").WithCause(err)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(ex.Nonce)) != 1 {
		return identity.Identity{}, time.Time{}, unauthenticated("the ID token nonce does not match this sign-in")
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		AuthTime      int64  `json:"auth_time"`
	}
	if err := idt.Claims(&claims); err != nil {
		return identity.Identity{}, time.Time{}, unauthenticated("the ID token claims are malformed").WithCause(err)
	}
	id := identity.Identity{Issuer: p.cfg.Issuer, Subject: idt.Subject, Email: claims.Email,
		EmailVerified: claims.EmailVerified, Name: claims.Name}
	if err := id.Validate(); err != nil {
		return identity.Identity{}, time.Time{}, unauthenticated("the ID token names no subject").WithCause(err)
	}
	var authTime time.Time
	if claims.AuthTime > 0 {
		authTime = time.Unix(claims.AuthTime, 0).UTC()
	}
	return id, authTime, nil
}

// authorizeURL builds an authorization URL with state, PKCE and extra options.
func authorizeURL(conf *oauth2.Config, req identity.AuthRequest, extra ...oauth2.AuthCodeOption) (string, error) {
	if req.State == "" || req.CodeChallenge == "" {
		return "", invalidArgument("a sign-in needs a state and a PKCE challenge")
	}
	opts := append([]oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("code_challenge", req.CodeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	}, extra...)
	return conf.AuthCodeURL(req.State, opts...), nil
}

// exchangeCode redeems an authorization code with its PKCE verifier.
func exchangeCode(ctx context.Context, conf *oauth2.Config, ex identity.Exchange) (*oauth2.Token, error) {
	if ex.CodeVerifier == "" {
		return nil, unauthenticated("a sign-in needs its PKCE verifier")
	}
	tok, err := conf.Exchange(ctx, ex.Code, oauth2.VerifierOption(ex.CodeVerifier))
	if err != nil {
		return nil, unauthenticated("the provider refused the authorization code").WithCause(err)
	}
	return tok, nil
}

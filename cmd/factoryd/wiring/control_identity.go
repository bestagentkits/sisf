package wiring

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/rbac"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/identity"
)

// Names of the sign-in providers; each one's callback is
// <external URL>/auth/callback/<name>.
const (
	ProviderOIDC   = "google"
	ProviderGitHub = "github"
)

// identityTimeout bounds one call to a sign-in provider or to GitHub's
// permission API.
const identityTimeout = 15 * time.Second

// discoveryWait bounds how long control waits at start for its OIDC
// provider to answer discovery. A provider that starts after control, such
// as the simulation's fake identity provider in control's own network
// namespace, or one briefly unreachable, is waited for; a misconfigured
// issuer still fails the start once the wait is over.
const discoveryWait = time.Minute

// discoverOIDC discovers the provider, trying again every interval while it
// is UNAVAILABLE, until wait has passed or ctx ends. Any other refusal, such
// as a provider reporting another issuer, fails at once.
func discoverOIDC(ctx context.Context, cfg auth.OIDCConfig, wait, interval time.Duration) (*auth.OIDCProvider, error) {
	deadline := time.Now().Add(wait)
	for {
		p, err := auth.NewOIDCProvider(ctx, cfg)
		if err == nil || !core.HasCode(err, core.CodeUnavailable) || !time.Now().Add(interval).Before(deadline) {
			return p, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(interval):
		}
	}
}

// signIn is the authentication service and what the surfaces show of it.
type signIn struct {
	service   *auth.Service
	providers []string
	// formOrigins are the provider origins a step-up form posts toward.
	formOrigins []string
}

// newSignIn builds the authentication service from cfg; a token may carry
// only the scopes of registered actions. An OIDC provider is
// discovered now, so a misconfigured issuer fails at start, not at the
// first sign-in.
func newSignIn(ctx context.Context, cfg IdentityConfig, scopes []string, clock core.Clock) (signIn, error) {
	client := &http.Client{Timeout: identityTimeout}
	var out signIn
	var providers []identity.Provider
	if cfg.OIDC != nil {
		p, err := discoverOIDC(ctx, auth.OIDCConfig{Name: ProviderOIDC, Issuer: cfg.OIDC.Issuer, ClientID: cfg.OIDC.ClientID,
			ClientSecret: cfg.OIDC.ClientSecret, RedirectURL: callback(cfg.ExternalURL, ProviderOIDC), HTTPClient: client, Clock: clock},
			discoveryWait, time.Second)
		if err != nil {
			return out, err
		}
		providers, out.providers = append(providers, p), append(out.providers, ProviderOIDC)
		out.formOrigins = append(out.formOrigins, origin(cfg.OIDC.Issuer))
	}
	if cfg.GitHub != nil {
		gh := auth.GitHubConfig{Name: ProviderGitHub, ClientID: cfg.GitHub.ClientID, ClientSecret: cfg.GitHub.ClientSecret,
			RedirectURL: callback(cfg.ExternalURL, ProviderGitHub), APIBase: cfg.GitHub.APIURL, HTTPClient: client}
		if web := strings.TrimRight(cfg.GitHub.WebURL, "/"); web != "" {
			gh.Issuer, gh.AuthURL, gh.TokenURL = web, web+"/login/oauth/authorize", web+"/login/oauth/access_token"
		}
		if cfg.GitHub.Issuer != "" {
			gh.Issuer = strings.TrimRight(cfg.GitHub.Issuer, "/")
		}
		p, err := auth.NewGitHubProvider(gh)
		if err != nil {
			return out, err
		}
		providers, out.providers = append(providers, p), append(out.providers, ProviderGitHub)
		out.formOrigins = append(out.formOrigins, origin(gh.AuthURL))
	}
	secure := strings.HasPrefix(cfg.ExternalURL, "https://")
	svc, err := auth.New(auth.Config{Providers: providers, Clock: clock, SecureCookies: secure, AllowedScopes: scopes})
	if err != nil {
		return out, err
	}
	out.service = svc
	return out, nil
}

func callback(external, provider string) string { return external + "/auth/callback/" + provider }

// origin returns scheme://host of a URL, or "" when it has none.
func origin(raw string) string {
	if raw == "" {
		raw = "https://github.com"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// newAuthorizer builds the role authority: local grants (the Owner) plus
// each person's GitHub permission on the factory's repository, read with
// the control zone's own GitHub token, never with the person's.
func newAuthorizer(cfg IdentityConfig, signIn *auth.Service, repo github.RepoRef, installation identity.Installation, githubToken core.SecretValue,
	clock core.Clock) (*rbac.Authorizer, error) {
	grants := rbac.NewGrants()
	for _, owner := range cfg.Owners {
		if err := grants.Add(auth.ActorID(owner), core.ActorHuman, core.RoleOwner); err != nil {
			return nil, err
		}
	}
	rc := rbac.Config{Grants: grants, Sessions: signIn, Clock: clock, DefaultRepo: repo.Owner + "/" + repo.Name,
		Installations: staticInstallation{installation}}
	if cfg.GitHub != nil && !githubToken.IsZero() {
		api := cfg.GitHub.APIURL
		if api == "" {
			api = github.DefaultBaseURL
		}
		rc.Accounts = signIn
		rc.Membership = &rbac.GitHubMembership{APIBase: strings.TrimRight(api, "/"), HTTPClient: &http.Client{Timeout: identityTimeout},
			Token: func(context.Context, string, string) (core.SecretValue, error) { return githubToken, nil }}
	}
	return rbac.New(rc)
}

// staticInstallation is the factory's one installation, on its repository.
type staticInstallation struct{ inst identity.Installation }

// Installation returns the installation on owner/repo, or NOT_FOUND.
func (s staticInstallation) Installation(_ context.Context, owner, repo string) (identity.Installation, error) {
	if !s.inst.Covers(owner, repo) {
		return identity.Installation{}, core.NewError(core.CodeNotFound, "the factory is not installed on "+owner+"/"+repo)
	}
	return s.inst, nil
}

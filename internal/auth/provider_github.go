package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"golang.org/x/oauth2"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
)

// GitHubIssuer is the issuer of identities from github.com.
const GitHubIssuer = "https://github.com"

// GitHubConfig configures GitHub OAuth login. Signing in with GitHub reads
// who the person is and nothing else: the user access token is used once for
// GET /user and then dropped, and it grants no repository permission.
type GitHubConfig struct {
	// Name defaults to "github".
	Name string
	// Issuer defaults to GitHubIssuer. A fake or GitHub Enterprise sets its own.
	Issuer string
	// AuthURL, TokenURL and APIBase default to github.com's.
	AuthURL      string
	TokenURL     string
	APIBase      string
	ClientID     string
	ClientSecret core.SecretValue
	RedirectURL  string
	HTTPClient   *http.Client
}

// GitHubProvider signs people in with GitHub OAuth. It implements identity.Provider.
type GitHubProvider struct{ cfg GitHubConfig }

// NewGitHubProvider validates cfg and fills in github.com defaults.
func NewGitHubProvider(cfg GitHubConfig) (*GitHubProvider, error) {
	if cfg.Name == "" {
		cfg.Name = "github"
	}
	if cfg.Issuer == "" {
		cfg.Issuer = GitHubIssuer
	}
	if cfg.AuthURL == "" {
		cfg.AuthURL = "https://github.com/login/oauth/authorize"
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = "https://github.com/login/oauth/access_token"
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.github.com"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.ClientID == "" || cfg.RedirectURL == "" {
		return nil, invalidArgument("a GitHub provider needs a client ID and redirect URL")
	}
	if (identity.Identity{Issuer: cfg.Issuer, Subject: "x"}).Validate() != nil {
		return nil, invalidArgument("a GitHub provider needs an absolute issuer URL")
	}
	return &GitHubProvider{cfg: cfg}, nil
}

// Name is the configured provider name.
func (p *GitHubProvider) Name() string { return p.cfg.Name }

// Issuer is the issuer every identity from this provider carries.
func (p *GitHubProvider) Issuer() string { return p.cfg.Issuer }

func (p *GitHubProvider) oauth(redirect string) *oauth2.Config {
	if redirect == "" {
		redirect = p.cfg.RedirectURL
	}
	return &oauth2.Config{ClientID: p.cfg.ClientID, ClientSecret: string(p.cfg.ClientSecret.Reveal()), RedirectURL: redirect,
		Endpoint: oauth2.Endpoint{AuthURL: p.cfg.AuthURL, TokenURL: p.cfg.TokenURL}}
}

// AuthorizeURL returns where to send the browser to sign in. GitHub OAuth
// has no nonce; the state and PKCE verifier bind the sign-in instead.
func (p *GitHubProvider) AuthorizeURL(req identity.AuthRequest) (string, error) {
	return authorizeURL(p.oauth(req.RedirectURI), req)
}

// Exchange redeems the code and reads the user it belongs to. The subject is
// GitHub's numeric user ID, which survives a change of login; the email
// GitHub reports is not verified by this flow and is kept as contact only.
func (p *GitHubProvider) Exchange(ctx context.Context, ex identity.Exchange) (identity.Identity, error) {
	if ex.Code == "" {
		return identity.Identity{}, unauthenticated("a sign-in needs a code")
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, p.cfg.HTTPClient)
	tok, err := exchangeCode(ctx, p.oauth(ex.RedirectURI), ex)
	if err != nil {
		return identity.Identity{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.APIBase+"/user", nil)
	if err != nil {
		return identity.Identity{}, core.NewError(core.CodeInternal, "the GitHub user request could not be built").WithCause(err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	tok.SetAuthHeader(req)
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return identity.Identity{}, core.NewError(core.CodeUnavailable, "GitHub could not be reached").WithCause(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return identity.Identity{}, unauthenticated("GitHub refused the user access token (status " + strconv.Itoa(resp.StatusCode) + ")")
	}
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&user); err != nil {
		return identity.Identity{}, unauthenticated("GitHub returned a malformed user").WithCause(err)
	}
	if user.ID <= 0 || user.Login == "" {
		return identity.Identity{}, unauthenticated("GitHub returned no user ID")
	}
	return identity.Identity{Issuer: p.cfg.Issuer, Subject: strconv.FormatInt(user.ID, 10),
		Email: user.Email, Login: user.Login, Name: user.Name}, nil
}

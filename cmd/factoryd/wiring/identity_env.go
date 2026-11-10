package wiring

import (
	"net"
	"net/url"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The sign-in contract of the control role. Client secrets are secrets:
// they are wrapped when read and redacted from every log line.
const (
	// EnvExternalURL is the base URL people and MCP clients reach the
	// control server at; it is the OAuth issuer and the base of every
	// callback. It defaults to http://<listen address> on loopback.
	EnvExternalURL = "SISF_EXTERNAL_URL"
	// EnvOwnerIdentities lists, comma separated, the sign-in identities that
	// hold the Owner role, each as issuer#subject. The Owner is granted
	// locally only; no GitHub permission makes an Owner.
	EnvOwnerIdentities = "SISF_OWNER_IDENTITIES"
	// EnvOIDCIssuer, EnvOIDCClientID and EnvOIDCClientSecret configure the
	// OpenID Connect sign-in, offered as "google".
	EnvOIDCIssuer       = "SISF_AUTH_OIDC_ISSUER"
	EnvOIDCClientID     = "SISF_AUTH_OIDC_CLIENT_ID"
	EnvOIDCClientSecret = "SISF_AUTH_OIDC_CLIENT_SECRET"
	// EnvGitHubLoginURL, EnvGitHubLoginClientID and
	// EnvGitHubLoginClientSecret configure GitHub sign-in, offered as
	// "github". EnvGitHubLoginURL is the web origin of the sign-in pages
	// (github.com unless set); EnvGitHubLoginAPIURL is the REST API the
	// login and the repository permission are read from.
	EnvGitHubLoginURL          = "SISF_AUTH_GITHUB_URL"
	EnvGitHubLoginAPIURL       = "SISF_AUTH_GITHUB_API_URL"
	EnvGitHubLoginClientID     = "SISF_AUTH_GITHUB_CLIENT_ID"
	EnvGitHubLoginClientSecret = "SISF_AUTH_GITHUB_CLIENT_SECRET"
)

// The simulation profile signs people in at the fake identity provider of
// "sisf sim oidc", which also stands for GitHub's sign-in and permission API.
const (
	SimOIDCIssuer   = "http://127.0.0.1:7468"
	SimOIDCClientID = "sisf-local"
	SimOwnerSubject = "owner"
	// simGitHubAPIPath is where the fake serves its GitHub REST dialect.
	simGitHubAPIPath = "/api"
	// SimGitHubIssuerPath, appended to the fake's issuer, is the issuer of
	// the GitHub identities it signs in, so they never share the issuer of
	// its OIDC identities.
	SimGitHubIssuerPath = "/github"
)

// IdentitySecretEnv names the sign-in variables whose values are secrets,
// for the process's log redactor.
var IdentitySecretEnv = []string{EnvOIDCClientSecret, EnvGitHubLoginClientSecret}

// OIDCLogin is one OpenID Connect sign-in provider.
type OIDCLogin struct {
	Issuer       string
	ClientID     string
	ClientSecret core.SecretValue
}

// GitHubLogin is GitHub sign-in. Empty URLs mean github.com's.
type GitHubLogin struct {
	// WebURL is the origin of the authorize and token pages.
	WebURL string
	// APIURL is the REST API; the person's login and their permission on
	// the factory's repository are read there.
	APIURL string
	// Issuer is the issuer GitHub identities carry. Empty means WebURL, or
	// github.com's; it must differ from every other provider's issuer.
	Issuer       string
	ClientID     string
	ClientSecret core.SecretValue
}

// IdentityConfig is how people sign in to the control zone and who the
// Owner is. Auth state (sessions, tokens, OAuth grants) is held in memory: a
// restart signs everyone out and voids every token, the safe failure.
type IdentityConfig struct {
	ExternalURL string
	OIDC        *OIDCLogin
	GitHub      *GitHubLogin
	// Owners are the sign-in identities granted the Owner role.
	Owners []identity.Identity
}

// IdentityFromEnv reads the sign-in contract. The simulation profile
// defaults it to the fake identity provider with "owner" as the Owner; a
// real install must name at least one provider and one Owner.
func IdentityFromEnv(getenv func(string) string, m *manifest.Manifest, listen string) (IdentityConfig, error) {
	sim := SimulationProfile(m)
	cfg := IdentityConfig{ExternalURL: strings.TrimRight(strings.TrimSpace(getenv(EnvExternalURL)), "/")}
	if cfg.ExternalURL == "" {
		host, _, err := net.SplitHostPort(listen)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			return cfg, core.NewError(core.CodeInvalidArgument,
				EnvExternalURL+" must be set when the control server does not listen on a loopback address")
		}
		cfg.ExternalURL = "http://" + listen
	}
	if u, err := url.Parse(cfg.ExternalURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" {
		return cfg, core.NewError(core.CodeInvalidArgument, EnvExternalURL+" must be an http(s) origin without a path")
	}
	issuer := strings.TrimSpace(getenv(EnvOIDCIssuer))
	if issuer == "" && sim {
		issuer = SimOIDCIssuer
	}
	if issuer != "" {
		clientID := strings.TrimSpace(getenv(EnvOIDCClientID))
		if clientID == "" && !sim {
			return cfg, missing(EnvOIDCClientID)
		}
		cfg.OIDC = &OIDCLogin{Issuer: issuer, ClientID: orDefault(clientID, SimOIDCClientID), ClientSecret: secret(getenv(EnvOIDCClientSecret))}
	}
	if id := strings.TrimSpace(getenv(EnvGitHubLoginClientID)); id != "" || sim {
		cfg.GitHub = &GitHubLogin{WebURL: strings.TrimSpace(getenv(EnvGitHubLoginURL)), APIURL: strings.TrimSpace(getenv(EnvGitHubLoginAPIURL)),
			ClientID: orDefault(id, SimOIDCClientID), ClientSecret: secret(getenv(EnvGitHubLoginClientSecret))}
		if sim && cfg.GitHub.WebURL == "" {
			cfg.GitHub.WebURL, cfg.GitHub.Issuer = issuer, issuer+SimGitHubIssuerPath
		}
		if sim && cfg.GitHub.APIURL == "" {
			cfg.GitHub.APIURL = issuer + simGitHubAPIPath
		}
	}
	if cfg.OIDC == nil && cfg.GitHub == nil {
		return cfg, core.NewError(core.CodeInvalidArgument, "name a sign-in provider: "+EnvOIDCIssuer+" or "+EnvGitHubLoginClientID)
	}
	owners, err := parseOwners(getenv(EnvOwnerIdentities))
	if err != nil {
		return cfg, err
	}
	if len(owners) == 0 && sim && cfg.OIDC != nil {
		owners = []identity.Identity{{Issuer: cfg.OIDC.Issuer, Subject: SimOwnerSubject}}
	}
	if len(owners) == 0 {
		return cfg, missing(EnvOwnerIdentities)
	}
	cfg.Owners = owners
	return cfg, nil
}

// parseOwners reads issuer#subject pairs.
func parseOwners(raw string) ([]identity.Identity, error) {
	var out []identity.Identity
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		i := strings.LastIndex(item, "#")
		id := identity.Identity{Subject: item[i+1:]}
		if i > 0 {
			id.Issuer = item[:i]
		}
		if i <= 0 || id.Validate() != nil {
			return nil, core.NewError(core.CodeInvalidArgument, EnvOwnerIdentities+" must list identities as issuer#subject")
		}
		out = append(out, id)
	}
	return out, nil
}

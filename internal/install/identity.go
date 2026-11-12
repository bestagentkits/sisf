package install

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// PortFakeOIDC is where the fake identity provider of the simulation and
// development profiles listens, on loopback (published from the control
// side in Docker mode). It is the port of wiring.SimOIDCIssuer.
const PortFakeOIDC = 7468

// Extension keys of the sign-in settings in factory.yaml. They are not
// secret; the client secrets live in the control zone's env file only.
const (
	extExternalURL         = "external_url"
	extOwnerIdentities     = "owner_identities"
	extOIDCIssuer          = "auth_oidc_issuer"
	extOIDCClientID        = "auth_oidc_client_id"
	extGitHubLoginClientID = "auth_github_client_id"
	extGitHubLoginURL      = "auth_github_url"
	extGitHubLoginAPIURL   = "auth_github_api_url"
	// extDev marks a development install: Standalone on loopback, signing
	// in at the fake identity provider.
	extDev = "dev"
)

// Identity is how people sign in to the control zone and who the Owner is.
// The client secrets are secrets and are never printed.
type Identity struct {
	// ExternalURL is the origin people and MCP clients reach control at.
	ExternalURL string
	// Owners are issuer#subject identities holding the Owner role.
	Owners []string
	// OIDCIssuer and OIDCClientID configure OpenID Connect sign-in.
	OIDCIssuer, OIDCClientID string
	OIDCClientSecret         core.SecretValue
	// GitHubClientID configures GitHub sign-in; GitHubURL and GitHubAPIURL
	// default to github.com when empty.
	GitHubClientID          string
	GitHubClientSecret      core.SecretValue
	GitHubURL, GitHubAPIURL string
}

// defaultExternalURL is control's published loopback address.
func defaultExternalURL() string { return "http://" + addr(PortControl) }

// identityValue is what a sign-in setting written into an env file or the
// Compose file may hold: URL characters and '#', no quote, space or '$'.
var identityValue = regexp.MustCompile(`^[A-Za-z0-9_@:/.,=+?&%#~-]*$`)

// fakeIdentity reports whether raw names the fake identity provider: any
// loopback address on its port.
func fakeIdentity(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Port() != strconv.Itoa(PortFakeOIDC) {
		return false
	}
	return loopbackHost(u.Hostname())
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// normalize checks the sign-in settings and fills the defaults. The
// simulation and the development profile sign in at the fake identity
// provider and take no provider settings; any other install must name a
// real provider over https and at least one Owner, and may never point at
// the fake.
func (id Identity) normalize(fake bool) (Identity, error) {
	if id.ExternalURL == "" {
		id.ExternalURL = defaultExternalURL()
	}
	id.ExternalURL = strings.TrimRight(id.ExternalURL, "/")
	if err := checkExternalURL(id.ExternalURL, fake); err != nil {
		return id, err
	}
	if fake {
		if id.OIDCIssuer != "" || id.OIDCClientID != "" || !id.OIDCClientSecret.IsZero() || id.GitHubClientID != "" ||
			!id.GitHubClientSecret.IsZero() || id.GitHubURL != "" || id.GitHubAPIURL != "" {
			return id, invalidf("the simulation and development profiles sign in at the fake identity provider; do not pass sign-in provider settings")
		}
		id.OIDCIssuer, id.OIDCClientID = wiring.SimOIDCIssuer, wiring.SimOIDCClientID
		if len(id.Owners) == 0 {
			id.Owners = []string{wiring.SimOIDCIssuer + "#" + wiring.SimOwnerSubject}
		}
	} else if err := id.checkProviders(); err != nil {
		return id, err
	}
	if len(id.Owners) == 0 {
		return id, invalidf("name the Owner's sign-in identity as issuer#subject (--owner)")
	}
	for _, owner := range id.Owners {
		if err := checkOwner(owner, fake); err != nil {
			return id, err
		}
	}
	for _, v := range []string{id.ExternalURL, id.OIDCIssuer, id.OIDCClientID, id.GitHubClientID, id.GitHubURL, id.GitHubAPIURL, strings.Join(id.Owners, ",")} {
		if !identityValue.MatchString(v) {
			return id, invalidf("the sign-in setting %q has characters an env or Compose file cannot carry safely", v)
		}
	}
	return id, nil
}

// extensions records the non-secret settings in the manifest.
func (id Identity) extensions(ext map[string]any) {
	set := func(key, value string) {
		if value != "" {
			ext[key] = value
		}
	}
	set(extExternalURL, id.ExternalURL)
	set(extOwnerIdentities, strings.Join(id.Owners, ","))
	set(extOIDCIssuer, id.OIDCIssuer)
	set(extOIDCClientID, id.OIDCClientID)
	set(extGitHubLoginClientID, id.GitHubClientID)
	set(extGitHubLoginURL, id.GitHubURL)
	set(extGitHubLoginAPIURL, id.GitHubAPIURL)
}

// identityOf reads the non-secret sign-in settings of an install.
func identityOf(m *manifest.Manifest) Identity {
	get := func(key string) string { v, _ := m.Extensions[key].(string); return v }
	id := Identity{
		ExternalURL: get(extExternalURL), OIDCIssuer: get(extOIDCIssuer), OIDCClientID: get(extOIDCClientID),
		GitHubClientID: get(extGitHubLoginClientID), GitHubURL: get(extGitHubLoginURL), GitHubAPIURL: get(extGitHubLoginAPIURL),
	}
	if owners := get(extOwnerIdentities); owners != "" {
		id.Owners = strings.Split(owners, ",")
	}
	return id
}

// usesFake reports whether the install signs in at the fake identity
// provider, which then runs as one of its units.
func (id Identity) usesFake() bool {
	return fakeIdentity(id.OIDCIssuer) || fakeIdentity(id.GitHubURL) || fakeIdentity(id.GitHubAPIURL)
}

// controlEnv is the sign-in contract of the control process, by name.
func (id Identity) controlEnv() []string {
	out := []string{wiring.EnvExternalURL + "=" + id.ExternalURL, wiring.EnvOwnerIdentities + "=" + strings.Join(id.Owners, ",")}
	add := func(name, value string) {
		if value != "" {
			out = append(out, name+"="+value)
		}
	}
	add(wiring.EnvOIDCIssuer, id.OIDCIssuer)
	add(wiring.EnvOIDCClientID, id.OIDCClientID)
	add(wiring.EnvGitHubLoginClientID, id.GitHubClientID)
	add(wiring.EnvGitHubLoginURL, id.GitHubURL)
	add(wiring.EnvGitHubLoginAPIURL, id.GitHubAPIURL)
	return out
}

// devInstall reports whether the manifest is a development install.
func devInstall(m *manifest.Manifest) bool { dev, _ := m.Extensions[extDev].(bool); return dev }

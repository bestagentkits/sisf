package install

import (
	"net/url"
	"strings"

	"github.com/bestagentkits/sisf/pkg/identity"
)

// checkProviders requires at least one real sign-in provider.
func (id Identity) checkProviders() error {
	if id.OIDCIssuer == "" && id.GitHubClientID == "" {
		return invalidf("a factory outside the simulation needs a sign-in provider: --oidc-issuer with --oidc-client-id, " +
			"or --github-login-client-id with its client secret (or use --profile sim, or --dev on loopback)")
	}
	if id.OIDCIssuer != "" || id.OIDCClientID != "" {
		if id.OIDCIssuer == "" || id.OIDCClientID == "" {
			return invalidf("OpenID Connect sign-in needs both --oidc-issuer and --oidc-client-id")
		}
		if err := checkProviderURL("--oidc-issuer", id.OIDCIssuer); err != nil {
			return err
		}
	}
	if id.GitHubClientID != "" || !id.GitHubClientSecret.IsZero() {
		if id.GitHubClientID == "" || id.GitHubClientSecret.IsZero() {
			return invalidf("GitHub sign-in needs --github-login-client-id and its client secret")
		}
	}
	for flag, raw := range map[string]string{"--github-login-url": id.GitHubURL, "--github-login-api-url": id.GitHubAPIURL} {
		if raw != "" {
			if err := checkProviderURL(flag, raw); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkProviderURL requires an https URL that is not the fake provider.
func checkProviderURL(flag, raw string) error {
	if fakeIdentity(raw) {
		return invalidf("%s names the fake identity provider, which only the simulation and development profiles may use", flag)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return invalidf("%s must be an https URL", flag)
	}
	return nil
}

// checkExternalURL requires an http(s) origin without a path; plain http
// only on loopback, and only loopback when the install signs in at the fake.
func checkExternalURL(raw string, fake bool) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return invalidf("the external URL must be an http(s) origin without a path, such as https://factory.example.com")
	}
	loopback := loopbackHost(u.Hostname())
	switch {
	case u.Scheme == "http" && !loopback:
		return invalidf("the external URL must be https unless it is a loopback address")
	case fake && !loopback:
		return invalidf("an install that signs in at the fake identity provider must be reached on loopback only")
	}
	return nil
}

// checkOwner parses one issuer#subject identity.
func checkOwner(raw string, fake bool) error {
	i := strings.LastIndex(raw, "#")
	if i <= 0 {
		return invalidf("the Owner %q must be issuer#subject", raw)
	}
	id := identity.Identity{Issuer: raw[:i], Subject: raw[i+1:]}
	if id.Validate() != nil {
		return invalidf("the Owner %q must be issuer#subject", raw)
	}
	if !fake && fakeIdentity(id.Issuer) {
		return invalidf("the Owner %q signs in at the fake identity provider, which only the simulation and development profiles may use", raw)
	}
	return nil
}

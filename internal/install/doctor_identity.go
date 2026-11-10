package install

import (
	"strings"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkIdentity checks how people sign in and what control holds of the
// edge: a provider and an Owner are configured, the external URL is sane,
// the fake identity provider is used only by the simulation and, with a
// loud warning, a development install, and control reads the edge's
// counters with a token of its own and holds no edge admin token.
func (in *Installer) checkIdentity(c *collector, m *manifest.Manifest) {
	id := identityOf(m)
	sim, dev := m.Extensions["profile"] == profileSim, devInstall(m)
	fakeAllowed := sim || dev
	switch {
	case id.OIDCIssuer == "" && id.GitHubClientID == "":
		c.fail("sign-in", "no sign-in provider is configured, so nobody can sign in: create the install again with --oidc-issuer or --github-login-client-id")
	case len(id.Owners) == 0:
		c.fail("sign-in", "no Owner identity is configured (owner_identities), so nobody holds the Owner role")
	default:
		bad := ""
		for _, o := range id.Owners {
			if err := checkOwner(o, fakeAllowed); err != nil {
				bad = err.Error()
			}
		}
		if bad != "" {
			c.fail("sign-in", "%s", bad)
		} else {
			c.ok("sign-in", "%s; Owner %s", providerNames(id), strings.Join(id.Owners, ", "))
		}
	}
	if err := checkExternalURL(id.ExternalURL, fakeAllowed && id.usesFake()); err != nil {
		c.fail("external url", "%q: %v", id.ExternalURL, err)
	} else {
		c.ok("external url", "%s", id.ExternalURL)
	}
	switch {
	case id.usesFake() && !fakeAllowed:
		c.fail("fake identity", "sign-in points at the fake identity provider on 127.0.0.1:%d, which only the simulation and development profiles may use", PortFakeOIDC)
	case dev:
		c.warn("fake identity", "%s", devWarning)
	case sim:
		c.ok("fake identity", "the simulation signs in at the fake identity provider on 127.0.0.1:%d", PortFakeOIDC)
	default:
		c.ok("fake identity", "not used")
	}
	in.checkEdgeTokens(c)
}

// providerNames lists the configured sign-in providers.
func providerNames(id Identity) string {
	var out []string
	if id.OIDCIssuer != "" {
		out = append(out, "OpenID Connect at "+id.OIDCIssuer)
	}
	if id.GitHubClientID != "" {
		out = append(out, "GitHub")
	}
	return strings.Join(out, " and ")
}

// checkEdgeTokens reports the edge's two tokens and what control holds.
func (in *Installer) checkEdgeTokens(c *collector) {
	problems, err := edgeTokenProblems(in.Layout)
	if err != nil {
		c.fail("edge tokens", "the env files cannot be read: %v", err)
		return
	}
	control, err := readEnvFile(in.Layout.RuntimeEnv(areaControl))
	if err != nil {
		c.fail("edge tokens", "control's env file cannot be read: %v", err)
		return
	}
	for _, key := range []string{"EDGE_ADMIN_TOKEN", "SISF_EDGE_ADMIN_URL"} {
		if _, held := control[key]; held {
			problems = append(problems, "control holds "+key+"; it may read the edge's counters only, with "+wiring.EnvEdgeCountersToken)
		}
	}
	if len(problems) > 0 {
		for _, p := range problems {
			c.fail("edge tokens", "%s", p)
		}
		return
	}
	c.ok("edge tokens", "control reads the counters with its own read-only token, which differs from the admin token; it holds no admin token")
}

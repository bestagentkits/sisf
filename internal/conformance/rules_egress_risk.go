package conformance

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// noteEgressRisks records the egress settings that widen what an agent can
// reach. Widening the allowlist and turning blocking off are the Owner's to
// choose, so they do not fail the manifest; each is shown as a risk beside
// the verdict.
func (c *checker) noteEgressRisks() {
	switch c.m.Egress.Mode {
	case core.EgressUnrestricted:
		c.risks = append(c.risks, Risk{Path: "egress.mode",
			Message: "egress blocking is turned off: a run may connect to any host, so a manipulated agent can send out whatever it can read"})
	case core.EgressAllowlist:
		for i, host := range c.m.Egress.Allow {
			if wideWildcard(host) {
				c.risks = append(c.risks, Risk{Path: fmt.Sprintf("egress.allow[%d]", i),
					Message: "the entry permits every host under a public suffix, which is close to no allowlist at all"})
			}
		}
	}
}

// registrySuffixes are the second-level labels under which country domains
// hand out names, as in co.uk and com.au.
var registrySuffixes = []string{"ac", "co", "com", "edu", "go", "gov", "mil", "ne", "net", "nom", "or", "org"}

// wideWildcard reports whether an allowlist entry permits every subdomain
// of a public suffix, such as "*.com" or "*.co.uk", rather than the
// subdomains of one organization's name.
func wideWildcard(host string) bool {
	base, wildcard := strings.CutPrefix(host, "*.")
	if !wildcard {
		return false
	}
	labels := strings.Split(base, ".")
	if len(labels) == 1 {
		return true
	}
	if len(labels) != 2 || len(labels[1]) != 2 {
		return false
	}
	return slices.Contains(registrySuffixes, labels[0])
}

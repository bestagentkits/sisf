package egress

import (
	"regexp"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// normalizeHost returns the host lowercased and without a trailing dot,
// or false when it is not a plain DNS name.
func normalizeHost(host string) (string, bool) {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	if h == "" || len(h) > 253 {
		return "", false
	}
	for _, label := range strings.Split(h, ".") {
		if !hostLabel.MatchString(label) {
			return "", false
		}
	}
	return h, true
}

// looksNumeric reports whether a name is an IP address in disguise, such as
// 127.1, 2130706433 or 0x7f.0.0.1, which some resolvers read as an address.
// A real host name never ends in a label of digits only.
func looksNumeric(host string) bool {
	labels := strings.Split(host, ".")
	last := labels[len(labels)-1]
	if strings.Trim(last, "0123456789") == "" {
		return true
	}
	for _, label := range labels {
		if strings.HasPrefix(label, "0x") {
			return true
		}
	}
	return false
}

// Allows reports whether the policy lets a connection to host through: in
// unrestricted mode every host, otherwise a host on the list or below a
// "*." entry. A wildcard does not cover the domain itself.
func Allows(p core.EgressPolicy, host string) bool {
	if p.Mode == core.EgressUnrestricted {
		return true
	}
	h, ok := normalizeHost(host)
	if !ok {
		return false
	}
	for _, entry := range p.Allow {
		if suffix, wild := strings.CutPrefix(entry, "*."); wild {
			if strings.HasSuffix(h, "."+suffix) {
				return true
			}
		} else if h == entry {
			return true
		}
	}
	return false
}

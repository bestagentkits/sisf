package core

import "regexp"

// EgressMode says whether outbound traffic from agent sandboxes is filtered.
type EgressMode string

const (
	// EgressAllowlist blocks and logs every destination not on the list.
	// It is the default.
	EgressAllowlist EgressMode = "allowlist"
	// EgressUnrestricted turns blocking off. Only the Owner can choose it,
	// through the approval pipeline and with a risk warning.
	EgressUnrestricted EgressMode = "unrestricted"
)

// Valid reports whether m is a known mode.
func (m EgressMode) Valid() bool { return m == EgressAllowlist || m == EgressUnrestricted }

// EgressPolicy is the outbound network policy for agent sandboxes. It is the
// Owner's decision: an agent can neither widen it nor switch it off for
// itself, and every change is planned, approved, executed and audited.
type EgressPolicy struct {
	Mode EgressMode `json:"mode" yaml:"mode"`
	// Allow lists permitted host names. A leading "*." also permits every
	// subdomain. The list is kept while blocking is off so that it can be
	// switched back on unchanged.
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`
}

var egressHostPattern = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// Validate rejects unknown modes and entries that are not plain lowercase
// host names, such as URLs or a bare wildcard that would allow everything.
func (p EgressPolicy) Validate() error {
	if !p.Mode.Valid() {
		return invalid("unknown egress mode %q", string(p.Mode))
	}
	for _, host := range p.Allow {
		if !egressHostPattern.MatchString(host) {
			return invalid("egress entry %q must be a lowercase host name, optionally prefixed with *.", host)
		}
	}
	return nil
}

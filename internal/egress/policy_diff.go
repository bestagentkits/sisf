package egress

import (
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Change is what a proposed policy changes against the one in force.
type Change struct {
	From    core.EgressMode `json:"from"`
	To      core.EgressMode `json:"to"`
	Added   []string        `json:"added,omitempty"`
	Removed []string        `json:"removed,omitempty"`
}

// Compare lists what proposed changes against current.
func Compare(current, proposed core.EgressPolicy) Change {
	cur, next := Normalize(current), Normalize(proposed)
	c := Change{From: cur.Mode, To: next.Mode}
	for _, host := range next.Allow {
		if !slices.Contains(cur.Allow, host) {
			c.Added = append(c.Added, host)
		}
	}
	for _, host := range cur.Allow {
		if !slices.Contains(next.Allow, host) {
			c.Removed = append(c.Removed, host)
		}
	}
	return c
}

// Empty reports whether nothing changes.
func (c Change) Empty() bool { return c.From == c.To && len(c.Added) == 0 && len(c.Removed) == 0 }

// TurnsOffBlocking reports whether the change stops blocking destinations.
func (c Change) TurnsOffBlocking() bool {
	return c.To == core.EgressUnrestricted && c.From != core.EgressUnrestricted
}

// Widens reports whether the change lets agents reach a destination they
// could not reach before, or could reach once blocking is back on. Any
// added entry counts, also while blocking is off, and so does turning
// blocking off.
func (c Change) Widens() bool { return c.TurnsOffBlocking() || len(c.Added) > 0 }

// RiskWarning is the warning the Owner reads before approving a change
// that widens the policy or turns blocking off, and "" for any other change.
func RiskWarning(c Change) string {
	var parts []string
	if c.TurnsOffBlocking() {
		parts = append(parts, "Turning off egress blocking lets agents send data to any public host, including hosts an injected instruction names; only the guard against private, loopback and metadata addresses remains.")
	}
	if len(c.Added) > 0 {
		parts = append(parts, "Widening the egress allowlist lets agents send data to "+strings.Join(c.Added, ", ")+"; a *. entry covers every subdomain.")
	}
	return strings.Join(parts, " ")
}

// String renders the change as a short diff for the approval request.
func (c Change) String() string {
	var b strings.Builder
	if c.From != c.To {
		b.WriteString("mode: " + string(c.From) + " -> " + string(c.To) + "\n")
	}
	for _, host := range c.Added {
		b.WriteString("+ " + host + "\n")
	}
	for _, host := range c.Removed {
		b.WriteString("- " + host + "\n")
	}
	return b.String()
}

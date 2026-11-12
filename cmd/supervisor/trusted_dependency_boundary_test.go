package main

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/supervisor/trustedclosure"
)

// The supervisor is the trust root and holds the release keys, so it must
// not link the control unit, whose code faces people and the network
// (sign-in, the dashboard, MCP): a defect there never runs inside the
// supervisor. Both are protected from self-publication all the same.
func TestTheSupervisorLinksNoPartOfTheControlUnit(t *testing.T) {
	t.Parallel()
	packages, err := trustedclosure.Closure("../..", "./cmd/supervisor")
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := trustedclosure.Dirs(packages)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range dirs {
		for _, forbidden := range []string{"cmd/factoryd/", "ui/", "internal/control/", "internal/mcp/", "internal/oauthas/"} {
			if strings.HasPrefix(dir, forbidden) {
				t.Errorf("the supervisor links %s, which belongs to the control unit (%s)", dir, forbidden)
			}
		}
	}
}

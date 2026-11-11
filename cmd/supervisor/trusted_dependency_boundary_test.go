package main

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/supervisor/trustedclosure"
)

// The supervisor is the trust root: everything it links is protected from
// self-publication, so it must not link the control unit, whose dashboard
// is the part of Zone 3 the factory is meant to update by itself.
func TestTheSupervisorLinksNoPartOfTheControlUnit(t *testing.T) {
	t.Parallel()
	packages, err := trustedclosure.Packages("../..")
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

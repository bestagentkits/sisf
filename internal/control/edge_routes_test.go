package control_test

import (
	"regexp"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/templates"
)

// The edge forwards outside traffic to the control zone only on paths the
// control zone serves to the outside. A route to a path it has no handler
// for would hand that path to whatever the control zone answers there.
func TestTheEdgeForwardsToTheControlZoneOnlyItsPublicPaths(t *testing.T) {
	t.Parallel()
	compose, err := templates.FS.ReadFile("compose/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	routes := regexp.MustCompile(`--route=(\S+)=http://control:`).FindAllSubmatch(compose, -1)
	if len(routes) == 0 {
		t.Fatal("the compose template routes nothing to the control zone")
	}
	for _, route := range routes {
		if prefix := string(route[1]); !slices.Contains(control.PublicPaths(), prefix) {
			t.Errorf("the edge forwards %s to the control zone, which serves no such public path", prefix)
		}
	}
}

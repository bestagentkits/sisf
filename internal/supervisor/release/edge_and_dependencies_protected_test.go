package release

import (
	"slices"
	"testing"
)

// The edge decides which release receives traffic, and the module files
// decide what every trusted binary links; neither is found by the closure of
// the supervisor's packages, so a change to them must still need approval.
func TestTheEdgeAndTheModulesDependenciesAreProtected(t *testing.T) {
	protected := DefaultProtectedPaths()
	for _, path := range []string{"cmd/edge/", "build/edge.Dockerfile", "go.mod", "go.sum"} {
		if !slices.Contains(protected, path) {
			t.Errorf("%s is not protected: a candidate could change it without approval", path)
		}
	}
	for _, changed := range []string{"cmd/edge/main.go", "build/edge.Dockerfile", "go.mod", "go.sum"} {
		if err := checkGovernance(protected, "app", []string{changed}); err == nil {
			t.Errorf("a candidate that changes %s passed the governance check", changed)
		}
	}
}

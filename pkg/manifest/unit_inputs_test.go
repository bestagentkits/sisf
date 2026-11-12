package manifest_test

import (
	"os"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// withUnitInputs returns the example manifest with release.units and
// release.unit_inputs replaced by the given YAML lines.
func withUnitInputs(t *testing.T, units string, inputs ...string) *manifest.Manifest {
	t.Helper()
	raw, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatal(err)
	}
	block := "  units: [" + units + "]\n  unit_inputs:\n"
	for _, line := range inputs {
		block += "    " + line + "\n"
	}
	text := strings.Replace(string(raw), "  units: [app]\n", block, 1)
	if text == string(raw) {
		t.Fatal("the example manifest no longer lists release.units as [app]")
	}
	m, err := manifest.Parse([]byte(text))
	if err != nil {
		t.Fatalf("the manifest must parse: %v", err)
	}
	return m
}

// Declared unit inputs are part of the manifest: they parse, validate and
// keep the patterns as written.
func TestDeclaredUnitInputsAreRead(t *testing.T) {
	m := withUnitInputs(t, "app", `app: ["*.go", "web/*.html", "build/App.Dockerfile"]`)
	if got := m.Release.UnitInputs[manifest.UnitApp]; strings.Join(got, ",") != "*.go,web/*.html,build/App.Dockerfile" {
		t.Fatalf("release.unit_inputs.app = %v", got)
	}
	if found := manifest.Validate(m); len(found) != 0 {
		t.Fatalf("a manifest declaring usable patterns has violations: %v", found)
	}
}

// A pattern that is not a clean repository path, or that matches whole
// trees by accident, is refused with a reason, at its place.
func TestUnusableUnitInputPatternsAreRefused(t *testing.T) {
	for pattern, reason := range map[string]string{
		`""`:              "is empty",
		`" app.go"`:       "space",
		`"/app.go"`:       "absolute",
		`"../app.go"`:     "clean repository path",
		`"web/../app.go"`: "clean repository path",
		`"./app.go"`:      "clean repository path",
		`"web/"`:          "clean repository path",
		`"web//x.html"`:   "clean repository path",
		`"web\\x.html"`:   `uses \`,
		`"examples/**"`:   "**",
		`"web/[a-"`:       "not a valid pattern",
		`"*"`:             "wildcards only",
		`"*/*"`:           "wildcards only",
		`"?/[ab]*"`:       "wildcards only",
	} {
		found := manifest.Validate(withUnitInputs(t, "app", "app: ["+pattern+"]"))
		if len(found) != 1 || found[0].Path != "release.unit_inputs.app[0]" || !strings.Contains(found[0].Message, reason) {
			t.Errorf("%s: violations %v, want one at release.unit_inputs.app[0] saying %q", pattern, found, reason)
		}
	}
}

// Inputs may be declared only for a unit the factory publishes by itself.
func TestUnitInputsOfAUnitNotPublishedByTheFactoryAreRefused(t *testing.T) {
	found := manifest.Validate(withUnitInputs(t, "app", `control: ["ui/templates/*.html"]`))
	if len(found) != 1 || found[0].Path != "release.unit_inputs.control" || !strings.Contains(found[0].Message, "release.units") {
		t.Fatalf("violations %v, want one saying release.units does not list control", found)
	}
}

// A pattern is matched against the whole path, without regard to case, and
// * never crosses a /.
func TestAUnitInputPatternMatchesWholePathsAndStarStopsAtASlash(t *testing.T) {
	for _, c := range []struct {
		pattern, path string
		want          bool
	}{
		{"*.go", "app.go", true},
		{"*.go", "APP.GO", true},
		{"*.go", "cmd/app.go", false},
		{"examples/hello-app/*", "examples/hello-app/main.go", true},
		{"examples/hello-app/*", "examples/hello-app/synthetic/main.go", false},
		{"build/App.Dockerfile", "build/app.dockerfile", true},
		{"build/App.Dockerfile", "build/app.dockerfile.bak", false},
		{"web/*.html", "web/x.css", false},
	} {
		if got := manifest.MatchUnitInput(c.pattern, c.path); got != c.want {
			t.Errorf("MatchUnitInput(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

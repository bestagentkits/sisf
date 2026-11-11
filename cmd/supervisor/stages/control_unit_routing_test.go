package stages

import (
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/internal/supervisor/trustedclosure"
)

// moduleRoot is the repository root, seen from this package.
const moduleRoot = "../../.."

// The routed directories of the control unit are generated from what the
// control binary links and the application does not; a package factoryd
// starts or stops linking makes the checked-in list stale.
func TestTheControlUnitsDirectoriesAreGeneratedFromWhatItLinks(t *testing.T) {
	t.Parallel()
	packages, err := trustedclosure.ControlOnlyPackages(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	want, err := trustedclosure.Dirs(packages)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(controlUnitPackageDirs, want) {
		t.Fatalf("the generated list of the control unit's packages is stale; run go generate ./cmd/supervisor/stages/ and commit the result\nchecked in: %v\nlinked now: %v",
			controlUnitPackageDirs, want)
	}
}

// underProtected reports whether path is a protected path or lies below one.
func underProtected(path string) bool {
	for _, p := range release.DefaultProtectedPaths() {
		if path == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(path, p)) {
			return true
		}
	}
	return false
}

// A change to code that only the control unit links builds and observes
// the control unit, never the application, whose release would not run it.
// Code the application links stays the application's.
func TestCodeOnlyTheControlUnitLinksIsABuildOfTheControlUnit(t *testing.T) {
	t.Parallel()
	control, err := trustedclosure.Closure(moduleRoot, trustedclosure.ControlBinaries...)
	if err != nil {
		t.Fatal(err)
	}
	app, err := trustedclosure.Closure(moduleRoot, trustedclosure.ApplicationPackages...)
	if err != nil {
		t.Fatal(err)
	}
	controlDirs, err := trustedclosure.Dirs(control)
	if err != nil {
		t.Fatal(err)
	}
	appDirs, err := trustedclosure.Dirs(app)
	if err != nil {
		t.Fatal(err)
	}
	router := DefaultUnitRouter()
	for _, dir := range controlDirs {
		if slices.Contains(appDirs, dir) || underProtected(dir) {
			continue
		}
		if unit, err := router.UnitOf([]string{dir + "x.go"}); err != nil || unit != "control" {
			t.Errorf("%s is linked only by the control unit but routes to %q (%v)", dir, unit, err)
		}
	}
	for _, dir := range appDirs {
		if unit, err := router.UnitOf([]string{dir + "x.go"}); err != nil || unit != "app" {
			t.Errorf("%s is linked by the application but routes to %q (%v)", dir, unit, err)
		}
	}
	for _, path := range []string{"internal/mcp/x.go", "ui/dashboard/status.html", "internal/control/web/x.go", "cmd/factoryd/main.go"} {
		if unit, err := router.UnitOf([]string{path}); err != nil || unit != "control" {
			t.Errorf("%s routes to %q (%v), want control", path, unit, err)
		}
	}
}

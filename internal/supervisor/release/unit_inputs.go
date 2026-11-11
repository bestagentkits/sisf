package release

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

//go:generate go run ../trustedclosure/gentrustedpackages -root ../../.. -list app -out app_unit_package_dirs_generated.go -package release

// The rule a candidate the factory publishes by itself must meet is
// default-deny: it may change only paths that are inputs of the one unit it
// was built as, released and observed. Observing a unit vouches for what
// that unit consumes and for nothing else; every other path, whatever it is
// for, needs the Owner's approval. The protected paths (DefaultProtectedPaths)
// stay a second refusal on top: a path that is both an input and protected
// is refused.
//
// Paths are compared without regard to letter case, as governance compares
// them, so a case-insensitive file system or checkout cannot route or admit
// a path under another spelling.

// Units whose inputs are listed. Any other unit has no input the factory
// may change by itself.
const (
	appUnit     = "app"
	controlUnit = "control"
)

// appUnitFiles are the inputs of the application that are not Go package
// directories: how its image is built.
var appUnitFiles = []string{"build/hello-app.dockerfile"}

// controlUnitDir holds the dashboard's templates and static assets, the
// only inputs of the control unit a candidate may change by itself. The Go
// code of the control unit is not one of them: the installer and the
// simulation link the control unit's Go packages, so one observed release
// of the control unit cannot vouch for it. A template or a static asset is
// what a trial update of a non-sensitive part such as a read-only dashboard
// page changes.
const controlUnitDir = "ui/"

// goToolchainExtensions are the extensions of files the Go toolchain
// compiles, assembles or links when they sit in a package directory: none of
// them is a template or a static asset, whatever directory it is in.
var goToolchainExtensions = []string{
	".go", ".s", ".sx", ".c", ".h", ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".hxx",
	".m", ".f", ".f90", ".for", ".syso", ".swig", ".swigcxx",
}

// goToolchainFiles are the file names that change what the Go toolchain
// builds wherever they are.
var goToolchainFiles = []string{"go.mod", "go.sum", "go.work", "go.work.sum"}

// UnitOfPath returns the unit whose inputs include the repository path p,
// false when p is the input of no unit and so needs the Owner's approval. A
// path is the input of at most one unit.
func UnitOfPath(p string) (string, bool) {
	clean, ok := repoPath(p)
	if !ok {
		return "", false
	}
	clean = strings.ToLower(clean)
	switch {
	case isAppInput(clean):
		return appUnit, true
	case isControlInput(clean):
		return controlUnit, true
	}
	return "", false
}

// isAppInput reports whether the case-folded path is a file directly in a
// directory of a package the application links, or how its image is built.
// A subdirectory is another package, which the generated list names when
// the application links it.
func isAppInput(clean string) bool {
	if slices.Contains(appUnitFiles, clean) {
		return true
	}
	dir := path.Dir(clean) + "/"
	return slices.ContainsFunc(appUnitPackageDirs, func(d string) bool { return strings.ToLower(d) == dir })
}

// isControlInput reports whether the case-folded path is a template or a
// static asset of the dashboard: below ui/ and not a file the Go toolchain
// builds.
func isControlInput(clean string) bool {
	if !strings.HasPrefix(clean, controlUnitDir) {
		return false
	}
	base := path.Base(clean)
	return !slices.Contains(goToolchainFiles, base) && !slices.Contains(goToolchainExtensions, path.Ext(base))
}

// shownPaths is how many offending paths a refusal names.
const shownPaths = 3

// checkUnitInputs refuses, for the Owner's approval, a candidate built as
// unit that changes a path which is not an input of unit: what one observed
// release of unit can vouch for is only what unit consumes. A change mixing
// the inputs of two units is refused the same way.
func checkUnitInputs(unit string, changed []string) error {
	var outside []string
	for _, raw := range changed {
		if of, ok := UnitOfPath(raw); !ok || !strings.EqualFold(of, strings.TrimSpace(unit)) {
			outside = append(outside, strings.TrimSpace(raw))
		}
	}
	if len(outside) == 0 {
		return nil
	}
	named := strings.Join(outside[:min(len(outside), shownPaths)], ", ")
	if more := len(outside) - shownPaths; more > 0 {
		named += fmt.Sprintf(" and %d more", more)
	}
	err := core.NewError(core.CodeApprovalRequired, fmt.Sprintf(
		"the candidate changes %s, which the unit %q it releases does not consume; observing %q cannot vouch for them, "+
			"so they need the Owner's approval", named, unit, unit))
	err.Details = map[string]string{"unit": unit, "path": outside[0]}
	return err
}

// CheckSelfPublishedChange is the rule a candidate the factory publishes and
// merges by itself must meet, as the release governance applies it: it is
// not the supervisor, every path it changes is an input of unit, and none of
// them is on the protected list. A refusal has the code APPROVAL_REQUIRED.
// The merge applies it again with the unit recorded at build time.
func CheckSelfPublishedChange(protected []string, unit string, changed []string) error {
	return checkGovernance(protected, unit, changed)
}

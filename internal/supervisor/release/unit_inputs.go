package release

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

//go:generate go run ../trustedclosure/gentrustedpackages -root ../../.. -list app -out app_unit_package_dirs_generated.go -package release

// The rule a candidate the factory publishes by itself must meet is
// default-deny: it may change only paths that are inputs of the one unit it
// was built as, released and observed. Observing a unit vouches for what
// that unit consumes and for nothing else; every other path, whatever it is
// for, needs the Owner's approval. The protected paths (DefaultProtectedPaths)
// stay a second refusal on top: a path that is both an input and protected
// is refused, whoever declared it an input.
//
// Only the application has built-in inputs (isAppInput); every other unit,
// the control unit included, has none unless the Owner declares them in the
// installed manifest's release.unit_inputs, which replaces the built-in
// inputs of the units it names: a factory may work on a repository whose
// application is not laid out like this one. The control unit's templates
// and scripts are what the Owner reads before approving, so they are never
// built in, and the trusted binaries' closure protects them (ui/).
//
// Files the Go toolchain reads are inputs of the application alone: any
// other unit's code is linked by more than that unit (the installer and the
// simulation link the control unit's packages), so one observed release of
// it cannot vouch for them, declared or not.
//
// Paths are compared without regard to letter case, as governance compares
// them, so a case-insensitive file system or checkout cannot route or admit
// a path under another spelling.

// appUnit is the unit whose inputs are built in. controlUnit is named only
// because its Go code is a common thing to declare by mistake.
const (
	appUnit     = "app"
	controlUnit = "control"
)

// goToolchainExtensions are the extensions of files the Go toolchain
// compiles, assembles or links when they sit in a package directory: none of
// them is a template or a static asset, whatever directory it is in.
var goToolchainExtensions = []string{
	".go", ".s", ".sx", ".c", ".h", ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".hxx",
	".m", ".f", ".f90", ".for", ".syso", ".swig", ".swigcxx",
}

// goToolchainFiles are the file names that change what the Go toolchain
// builds wherever they are. None is a built-in input, even in a directory
// of the application: a module or workspace file there changes how the
// application's packages resolve their dependencies.
var goToolchainFiles = []string{"go.mod", "go.sum", "go.work", "go.work.sum"}

// UnitInputs are the inputs of each unit: the built-in ones, and the
// patterns the Owner declared in the installed manifest's
// release.unit_inputs, which replace the built-in inputs of the units they
// name. The zero value is the built-in inputs alone.
type UnitInputs struct {
	declared map[string][]string
}

// DefaultUnitInputs returns the built-in inputs of every unit.
func DefaultUnitInputs() UnitInputs { return UnitInputs{} }

// NewUnitInputs returns the inputs with the declared patterns of each unit
// in place of its built-in ones. A pattern manifest.UnitInputPatternProblem
// refuses is refused here as well, so a manifest that was not validated
// cannot widen a unit's inputs.
func NewUnitInputs(declared map[string][]string) (UnitInputs, error) {
	if len(declared) == 0 {
		return UnitInputs{}, nil
	}
	kept := make(map[string][]string, len(declared))
	for unit, patterns := range declared {
		for _, p := range patterns {
			if problem := manifest.UnitInputPatternProblem(p); problem != "" {
				return UnitInputs{}, core.NewError(core.CodeInvalidArgument, fmt.Sprintf("release.unit_inputs.%s: the pattern %q %s", unit, p, problem))
			}
		}
		kept[strings.ToLower(unit)] = slices.Clone(patterns)
	}
	return UnitInputs{declared: kept}, nil
}

// units returns the units that may have inputs: the application and those
// the manifest declares, sorted.
func (u UnitInputs) units() []string {
	units := []string{appUnit}
	for unit := range u.declared {
		if !slices.Contains(units, unit) {
			units = append(units, unit)
		}
	}
	slices.Sort(units)
	return units
}

// consumes reports whether unit consumes the case-folded clean path. Files
// the Go toolchain reads are inputs of the application alone, declared or
// built in.
func (u UnitInputs) consumes(unit, clean string) bool {
	patterns, declared := u.declared[unit]
	switch {
	case !declared && unit == appUnit:
		return isAppInput(clean)
	case unit != appUnit && goToolchainFile(clean):
		return false
	}
	return slices.ContainsFunc(patterns, func(p string) bool { return manifest.MatchUnitInput(p, clean) })
}

// consumers returns every unit whose inputs include the repository path p.
func (u UnitInputs) consumers(p string) []string {
	clean, ok := repoPath(p)
	if !ok {
		return nil
	}
	clean = strings.ToLower(clean)
	var of []string
	for _, unit := range u.units() {
		if u.consumes(unit, clean) {
			of = append(of, unit)
		}
	}
	return of
}

// UnitOf returns the one unit whose inputs include the repository path p,
// false when p is the input of no unit, or of more than one, and so needs
// the Owner's approval.
func (u UnitInputs) UnitOf(p string) (string, bool) {
	if of := u.consumers(p); len(of) == 1 {
		return of[0], true
	}
	return "", false
}

// isAppInput reports whether the case-folded path is a file directly in a
// directory of a package the application links, other than a module or
// workspace file. A subdirectory is another package, which the generated
// list names when the application links it.
func isAppInput(clean string) bool {
	if slices.Contains(goToolchainFiles, path.Base(clean)) {
		return false
	}
	dir := path.Dir(clean) + "/"
	return slices.ContainsFunc(appUnitPackageDirs, func(d string) bool { return strings.ToLower(d) == dir })
}

// goToolchainFile reports whether the case-folded path is a file the Go
// toolchain reads.
func goToolchainFile(clean string) bool {
	base := path.Base(clean)
	return slices.Contains(goToolchainFiles, base) || slices.Contains(goToolchainExtensions, path.Ext(base))
}

// shownPaths is how many offending paths a refusal names.
const shownPaths = 3

// namedPaths names the first shownPaths of paths and counts the rest.
func namedPaths(paths []string) string {
	named := strings.Join(paths[:min(len(paths), shownPaths)], ", ")
	if more := len(paths) - shownPaths; more > 0 {
		named += fmt.Sprintf(" and %d more", more)
	}
	return named
}

// checkUnitInputs refuses, for the Owner's approval, a candidate built as
// unit that changes a path which is not an input of unit, or that more than
// one unit consumes: what one observed release of unit can vouch for is
// only what unit alone consumes. A change mixing the inputs of two units is
// refused the same way.
func checkUnitInputs(inputs UnitInputs, unit string, changed []string) error {
	unit = strings.TrimSpace(unit)
	var outside, shared []string
	var sharedBy []string
	for _, raw := range changed {
		of := inputs.consumers(raw)
		switch {
		case len(of) > 1:
			shared = append(shared, strings.TrimSpace(raw))
			if sharedBy == nil {
				sharedBy = of
			}
		case len(of) == 0 || !strings.EqualFold(of[0], unit):
			outside = append(outside, strings.TrimSpace(raw))
		}
	}
	var err *core.Error
	switch {
	case len(outside) > 0:
		err = core.NewError(core.CodeApprovalRequired, fmt.Sprintf(
			"the candidate changes %s, which the unit %q it releases does not consume; observing %q cannot vouch for them, "+
				"so they need the Owner's approval", namedPaths(outside), unit, unit))
		err.Details = map[string]string{"unit": unit, "path": outside[0]}
	case len(shared) > 0:
		err = core.NewError(core.CodeApprovalRequired, fmt.Sprintf(
			"the candidate changes %s, which the units %s all consume; observing %q alone cannot vouch for them, "+
				"so they need the Owner's approval", namedPaths(shared), strings.Join(sharedBy, " and "), unit))
		err.Details = map[string]string{"unit": unit, "path": shared[0]}
	default:
		return nil
	}
	return err
}

// CheckSelfPublishedChange is the rule a candidate the factory publishes and
// merges by itself must meet, as the release governance applies it: it is
// not the supervisor, every path it changes is an input of unit alone, and
// none of them is on the protected list. A refusal has the code
// APPROVAL_REQUIRED. The merge applies it again with the unit recorded at
// build time.
func CheckSelfPublishedChange(protected []string, inputs UnitInputs, unit string, changed []string) error {
	return checkGovernance(protected, inputs, unit, changed)
}

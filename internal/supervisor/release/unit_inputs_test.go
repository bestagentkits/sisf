package release

import (
	"bytes"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/supervisor/trustedclosure"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The application's input directories are generated from what it links; a
// package the application starts or stops linking makes the checked-in
// list stale.
func TestTheApplicationsInputDirectoriesAreGeneratedFromWhatItLinks(t *testing.T) {
	t.Parallel()
	packages, err := trustedclosure.ApplicationPackages(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	want, err := trustedclosure.Dirs(packages)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(appUnitPackageDirs, want) {
		t.Fatalf("the generated list of the application's packages is stale; run go generate ./internal/supervisor/release/ and commit the result\nchecked in: %v\nlinked now: %v",
			appUnitPackageDirs, want)
	}
}

// trackedFiles lists every file git tracks in the module's repository; the
// test is skipped outside a git checkout.
func trackedFiles(t *testing.T) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = moduleRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("the files of the repository cannot be listed, as outside a git checkout: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		t.Skip("git lists no file: not a git checkout of the module")
	}
	return files
}

// Every tracked path is either one that needs the Owner's approval or an
// input of exactly one unit, compared without regard to letter case. Two
// paths that differ only in case classify alike, and no input of a unit is
// on the protected list.
func TestEveryPathOfTheRepositoryNeedsApprovalOrIsTheInputOfOneUnit(t *testing.T) {
	t.Parallel()
	byUnit := classifyTrackedFiles(t, DefaultUnitInputs())
	if byUnit[appUnit] == 0 || len(byUnit) != 1 {
		t.Fatalf("inputs per unit = %v, want some of the application's and none of any other unit", byUnit)
	}
}

// The same holds with inputs the manifest declares in place of the
// built-in ones: here the application also consumes its Dockerfile, and the
// runner unit the operator guides.
func TestEveryPathNeedsApprovalOrIsTheInputOfOneUnitWithDeclaredInputs(t *testing.T) {
	t.Parallel()
	declared := map[string][]string{
		appUnit:  {"examples/hello-app/*", "build/hello-app.Dockerfile"},
		"runner": {"docs/guides/*.md"},
	}
	inputs, err := NewUnitInputs(declared)
	if err != nil {
		t.Fatal(err)
	}
	if conflicts := UnitInputConflicts(declared, DefaultProtectedPaths()); len(conflicts) != 0 {
		t.Fatalf("the declared inputs conflict: %+v", conflicts)
	}
	byUnit := classifyTrackedFiles(t, inputs)
	if unit, ok := inputs.UnitOf("docs/guides/operations.md"); !ok || unit != "runner" {
		t.Fatalf("a guide is the input of %q (%v), want runner as declared", unit, ok)
	}
	if unit, ok := inputs.UnitOf("build/hello-app.Dockerfile"); !ok || unit != appUnit {
		t.Fatalf("the declared Dockerfile is the input of %q (%v), want app", unit, ok)
	}
	if byUnit[appUnit] == 0 || byUnit["runner"] == 0 {
		t.Fatalf("inputs per unit = %v, want some of both app and runner", byUnit)
	}
}

// classifyTrackedFiles checks every tracked path against inputs and counts
// the inputs of each unit.
func classifyTrackedFiles(t *testing.T, inputs UnitInputs) map[string]int {
	t.Helper()
	protected := DefaultProtectedPaths()
	byUnit := map[string]int{}
	for _, f := range trackedFiles(t) {
		if of := inputs.consumers(f); len(of) > 1 {
			t.Errorf("%s is an input of %v, want at most one unit", f, of)
		}
		unit, ok := inputs.UnitOf(f)
		if upper, upperOK := inputs.UnitOf(strings.ToUpper(f)); upper != unit || upperOK != ok {
			t.Errorf("%s classifies as %q (%v) and in upper case as %q (%v)", f, unit, ok, upper, upperOK)
		}
		if !ok {
			if err := checkGovernance(protected, inputs, testUnit, []string{f}); !core.HasCode(err, core.CodeApprovalRequired) {
				t.Errorf("%s is the input of no unit, but a candidate changing it = %v, want APPROVAL_REQUIRED", f, err)
			}
			continue
		}
		byUnit[unit]++
		for _, entry := range protected {
			if covers(entry, f) {
				t.Errorf("%s is an input of %s and protected by %s", f, unit, entry)
			}
		}
		if err := checkGovernance(protected, inputs, unit, []string{f}); err != nil {
			t.Errorf("%s is an input of %s, but a candidate of %s changing it = %v", f, unit, unit, err)
		}
	}
	return byUnit
}

// The paths that reach a trusted program, the build of one, or anything the
// observed unit does not consume need the Owner's approval, whichever unit
// the candidate names.
func TestPathsNoObservedUnitConsumesNeedApproval(t *testing.T) {
	t.Parallel()
	for _, p := range []string{
		"internal/conformance/x.go", "internal/testkit/fakeoidc/x.go", "build/sisf.Dockerfile", "build/factoryd.Dockerfile",
		"Makefile", "vendor/modules.txt", "cmd/factoryd/main.go", "docs/x.md", "tests/x_test.go", "go.mod",
		"README.md", "examples/hello-app/synthetic/synthetic.go", "examples/hello-app/cmd/hello-app-synthetic/main.go",
		"ui/ui.go", "ui/templates/x.go", "ui/static/x.s", "ui/static/x.syso", "ui/go.mod", "UI/Dashboard.GO",
		"ui/templates/dash-approval.html", "ui/static/app.js", "UI/X.html", "build/hello-app.Dockerfile",
		"examples/hello-app/go.mod", "examples/hello-app/go.sum", "examples/hello-app/go.work", "EXAMPLES/HELLO-APP/GO.WORK.SUM",
	} {
		if unit, ok := DefaultUnitInputs().UnitOf(p); ok {
			t.Errorf("%s is an input of %s, want the input of no unit", p, unit)
		}
		for _, unit := range []string{appUnit, controlUnit, "runner"} {
			if err := checkGovernance(DefaultProtectedPaths(), UnitInputs{}, unit, []string{p}); !core.HasCode(err, core.CodeApprovalRequired) {
				t.Errorf("a %s candidate changing %s = %v, want APPROVAL_REQUIRED", unit, p, err)
			}
		}
	}
}

// The built-in inputs, in any letter case, are the files of the
// application's own package directory other than module files. The
// application's Dockerfile and the dashboard's templates and scripts are
// not built in, and a unit other than the application has none.
func TestTheInputsOfEachUnit(t *testing.T) {
	t.Parallel()
	for p, want := range map[string]string{
		"examples/hello-app/main.go":     appUnit,
		"examples/hello-app/new_file.go": appUnit,
		"EXAMPLES/Hello-App/Main.GO":     appUnit,
		"examples/hello-app/README.md":   appUnit,
	} {
		if unit, ok := DefaultUnitInputs().UnitOf(p); !ok || unit != want {
			t.Errorf("%s is an input of %q (%v), want %s", p, unit, ok, want)
		}
	}
	if err := checkGovernance(DefaultProtectedPaths(), UnitInputs{}, "runner", []string{"examples/hello-app/main.go"}); !core.HasCode(err, core.CodeApprovalRequired) {
		t.Errorf("a runner candidate changing the application = %v, want APPROVAL_REQUIRED", err)
	}
}

// A refusal for want of the Owner names the first offending paths, says how
// many more there are and why they need the Owner; a candidate mixing the
// inputs of two units is refused the same way.
func TestARefusalNamesTheFirstPathsTheObservedUnitDoesNotConsume(t *testing.T) {
	t.Parallel()
	changed := []string{"examples/hello-app/main.go", "docs/a.md", "Makefile", "tests/x_test.go", "go.mod", "ui/x.html"}
	err := checkGovernance(nil, UnitInputs{}, appUnit, changed)
	var refused *core.Error
	if !errors.As(err, &refused) || refused.Code != core.CodeApprovalRequired {
		t.Fatalf("checkGovernance = %v, want APPROVAL_REQUIRED", err)
	}
	for _, want := range []string{"docs/a.md, Makefile, tests/x_test.go and 2 more", `unit "app"`, "does not consume", "Owner's approval"} {
		if !strings.Contains(refused.Message, want) {
			t.Errorf("the refusal %q does not say %q", refused.Message, want)
		}
	}
	if refused.Details["path"] != "docs/a.md" {
		t.Errorf("the refusal names %q as its path, want docs/a.md", refused.Details["path"])
	}
	mixed := checkGovernance(nil, UnitInputs{}, controlUnit, []string{"ui/templates/a.html", "examples/hello-app/main.go"})
	if !core.HasCode(mixed, core.CodeApprovalRequired) || !strings.Contains(mixed.Error(), "examples/hello-app/main.go") {
		t.Fatalf("a control candidate with an application input = %v, want APPROVAL_REQUIRED naming it", mixed)
	}
}

package release

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// sandboxInputs are the inputs of an application whose Go files sit at the
// repository root, as the sandbox repository lays it out.
func sandboxInputs(t *testing.T) UnitInputs {
	t.Helper()
	inputs, err := NewUnitInputs(map[string][]string{appUnit: {"*.go"}})
	if err != nil {
		t.Fatal(err)
	}
	return inputs
}

// Declared inputs replace the built-in inputs of their unit entirely; the
// built-in inputs of a unit left out stay.
func TestDeclaredInputsReplaceTheBuiltInInputsOfTheirUnit(t *testing.T) {
	t.Parallel()
	inputs := sandboxInputs(t)
	for p, want := range map[string]string{
		"app.go":                     appUnit,
		"RELEASE_REHEARSAL_FAULT.GO": appUnit,
		"ui/templates/a.html":        controlUnit,
		"examples/hello-app/main.go": "",
		"synthetic/main.go":          "",
		"go.mod":                     "",
	} {
		unit, ok := inputs.UnitOf(p)
		if (want == "") == ok || unit != want {
			t.Errorf("%s is the input of %q (%v), want %q", p, unit, ok, want)
		}
	}
	if err := checkGovernance(DefaultProtectedPaths(), inputs, appUnit, []string{"app.go", "release_rehearsal_fault.go"}); err != nil {
		t.Fatalf("a candidate of the declared inputs = %v", err)
	}
	err := checkGovernance(DefaultProtectedPaths(), inputs, appUnit, []string{"app.go", "examples/hello-app/main.go"})
	if !core.HasCode(err, core.CodeApprovalRequired) || !strings.Contains(err.Error(), "examples/hello-app/main.go") {
		t.Fatalf("a candidate also changing a built-in input the declaration replaced = %v", err)
	}
}

// A path that is both declared and protected is refused at release time
// whatever the manifest says: the protected list stays on top.
func TestADeclaredInputThatIsProtectedIsStillRefused(t *testing.T) {
	t.Parallel()
	inputs, err := NewUnitInputs(map[string][]string{appUnit: {"*.go", "go.mod", ".sisf/*"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"go.mod", ".sisf/factory.yaml", ".SISF/FACTORY.YAML"} {
		err := checkGovernance(DefaultProtectedPaths(), inputs, appUnit, []string{"app.go", p})
		if !core.HasCode(err, core.CodeApprovalRequired) || !strings.Contains(err.Error(), "trusted control surface") {
			t.Errorf("a candidate changing the declared but protected %s = %v", p, err)
		}
	}
}

// A path two units consume is refused at release time for the Owner,
// whichever of them the candidate releases.
func TestAPathTwoUnitsConsumeIsRefused(t *testing.T) {
	t.Parallel()
	inputs, err := NewUnitInputs(map[string][]string{appUnit: {"web/*"}, controlUnit: {"web/*.html"}})
	if err != nil {
		t.Fatal(err)
	}
	if unit, ok := inputs.UnitOf("web/index.html"); ok {
		t.Fatalf("a path two units consume routes to %s", unit)
	}
	for _, unit := range []string{appUnit, controlUnit} {
		err := checkGovernance(DefaultProtectedPaths(), inputs, unit, []string{"web/index.html"})
		if !core.HasCode(err, core.CodeApprovalRequired) || !strings.Contains(err.Error(), "app and control all consume") {
			t.Errorf("a candidate of %s changing a path both units consume = %v", unit, err)
		}
	}
	if unit, ok := inputs.UnitOf("web/app.js"); !ok || unit != appUnit {
		t.Fatalf("a path only the application consumes routes to %q (%v)", unit, ok)
	}
}

// Go code is never an input of the control unit, even when the manifest
// declares it: the installer and the simulation link it too.
func TestDeclaredGoCodeOfTheControlUnitIsNotAnInput(t *testing.T) {
	t.Parallel()
	inputs, err := NewUnitInputs(map[string][]string{controlUnit: {"ui/*"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inputs.UnitOf("ui/zz.go"); ok {
		t.Fatal("ui/zz.go is an input of control")
	}
	if unit, ok := inputs.UnitOf("ui/page.html"); !ok || unit != controlUnit {
		t.Fatalf("ui/page.html is the input of %q (%v), want control", unit, ok)
	}
}

// A pattern the manifest validation refuses is refused when the inputs are
// built, so a manifest that was never validated cannot widen them.
func TestUnusableDeclaredPatternsAreRefused(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"**", "*", "/app.go", "../x.go", ""} {
		if _, err := NewUnitInputs(map[string][]string{appUnit: {p}}); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("the pattern %q = %v, want INVALID_ARGUMENT", p, err)
		}
	}
}

// The conflicts a declaration has are found with a path that proves them,
// including two patterns of the same depth that share no spelling.
func TestUnitInputConflictsAreProvedWithAPath(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		declared map[string][]string
		kind     UnitInputConflictKind
		witness  string
	}{
		{map[string][]string{appUnit: {"*.go"}, controlUnit: {"a*"}}, ConflictOverlap, "a.go"},
		{map[string][]string{appUnit: {"examples/*/main.go"}}, "", ""},
		{map[string][]string{appUnit: {"web/*"}}, "", ""},
		{map[string][]string{appUnit: {"ui/*.html"}}, ConflictOverlap, "ui/x.html"},
		{map[string][]string{appUnit: {"ui/*.go"}}, "", ""},
		{map[string][]string{controlUnit: {"examples/hello-app/*"}}, ConflictOverlap, "examples/hello-app/"},
		{map[string][]string{appUnit: {"*.*"}}, ConflictProtected, "protected"},
		{map[string][]string{appUnit: {"*/*.go"}}, ConflictProtected, ""},
		{map[string][]string{controlUnit: {"ui/templates/*.html"}}, "", ""},
		{map[string][]string{controlUnit: {"ui/*"}}, ConflictControlGoCode, ".go"},
		{map[string][]string{controlUnit: {"ui/*.g?"}}, ConflictControlGoCode, "ui/x.go"},
		{map[string][]string{controlUnit: {"ui/*.[gh]"}}, ConflictControlGoCode, "ui/x.h"},
		{map[string][]string{controlUnit: {"ui/go.[ms]*"}}, ConflictControlGoCode, "go."},
	} {
		conflicts := UnitInputConflicts(c.declared, DefaultProtectedPaths())
		if c.kind == "" {
			// "examples/*/main.go" overlaps no built-in input: the
			// application's own directory is replaced by the declaration.
			if len(conflicts) != 0 {
				t.Errorf("%v: conflicts %+v, want none", c.declared, conflicts)
			}
			continue
		}
		found := false
		for _, conflict := range conflicts {
			if conflict.Kind == c.kind && strings.Contains(conflict.Reason, c.witness) {
				found = true
			}
		}
		if !found {
			t.Errorf("%v: conflicts %+v, want %s naming %q", c.declared, conflicts, c.kind, c.witness)
		}
	}
}

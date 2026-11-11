package stages

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// The unit of a change is the unit whose inputs it changes, in any letter
// case; a path that is the input of no unit decides nothing.
func TestTheUnitOfAChangeIsTheUnitWhoseInputsItChanges(t *testing.T) {
	t.Parallel()
	router := DefaultUnitRouter()
	for want, paths := range map[string][]string{
		"app":     {"examples/hello-app/main.go", "docs/x.md"},
		"control": {"UI/x.html", "Makefile"},
	} {
		if unit, err := router.UnitOf(paths); err != nil || unit != want {
			t.Errorf("%v routes to %q (%v), want %s", paths, unit, err, want)
		}
	}
	for _, paths := range [][]string{{"ui/templates/a.html", "examples/hello-app/main.go"}, {"Examples/Hello-App/x.go", "ui/static/a.css"}} {
		if unit, err := router.UnitOf(paths); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%v routes to %q (%v), want refused as spanning two units", paths, unit, err)
		}
	}
}

// A change of no unit's inputs is built only so that QA judges it and its
// pull request is opened: the release refuses it for the Owner, since
// nothing in it is an input of the unit it is built as.
func TestAChangeOfNoUnitsInputsIsBuiltForTheOwnerToJudge(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"cmd/factoryd/main.go", "ui/ui.go", "internal/conformance/x.go", "README.md"} {
		if unit, err := DefaultUnitRouter().UnitOf([]string{p}); err != nil || unit != judgedOnlyUnit {
			t.Errorf("%s routes to %q (%v), want %s", p, unit, err, judgedOnlyUnit)
		}
	}
	if _, err := DefaultUnitRouter().UnitOf(nil); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("an empty change = %v, want INVALID_ARGUMENT", err)
	}
}

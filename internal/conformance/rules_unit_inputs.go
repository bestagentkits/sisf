package conformance

import (
	"fmt"

	"github.com/bestagentkits/sisf/internal/supervisor/release"
)

// unitInputRules maps each kind of refused unit input pattern to its rule
// and to what fixes it.
var unitInputRules = map[release.UnitInputConflictKind]struct {
	rule Rule
	fix  string
}{
	release.ConflictProtected: {RuleUnitInputsProtected,
		"narrow the pattern so it names no protected path; a protected path is changed only by the Owner"},
	release.ConflictGoCode: {RuleUnitInputsGoCode,
		"narrow the pattern so it names no Go code or module file; only the application's inputs may be Go code"},
	release.ConflictOverlap: {RuleUnitInputsOverlap,
		"narrow one of the two units' patterns so that no path is the input of both"},
}

// checkUnitInputs refuses release.unit_inputs patterns the supervisor would
// not trust: one that can match a protected path, which a manifest cannot
// open to a candidate the factory publishes by itself, the dashboard's
// templates and scripts under ui/ among them; one of a unit other than the
// application that can match a file the Go toolchain reads, since more than
// that unit links such code; and one that can match an input of another
// unit, declared or built in. Each finding names a path the pattern
// matches.
func (c *checker) checkUnitInputs() {
	declared := make(map[string][]string, len(c.m.Release.UnitInputs))
	for unit, patterns := range c.m.Release.UnitInputs {
		declared[string(unit)] = patterns
	}
	for _, conflict := range release.UnitInputConflicts(declared, release.DefaultProtectedPaths()) {
		r := unitInputRules[conflict.Kind]
		c.add(r.rule, fmt.Sprintf("release.unit_inputs.%s[%d]", conflict.Unit, conflict.Index), r.fix,
			"the pattern %q %s", conflict.Pattern, conflict.Reason)
	}
}

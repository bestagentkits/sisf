package conformance

import "github.com/bestagentkits/sisf/pkg/manifest"

// checkLaunchers refuses a unit other than the supervisor, which has a rule
// of its own, that is identified by what it starts and starts through a
// shell or another launcher. The launcher runs a program named in an
// argument, often inside a script this check cannot read, so what the unit
// runs can be shown neither to differ from the supervisor's executable nor
// to lie outside the directories other units write.
func (c *checker) checkLaunchers() {
	for _, name := range manifest.UnitNames() {
		unit, ok := c.m.Units[name]
		if !ok || name == manifest.UnitSupervisor || !c.comparedByExecutable(unit) || !throughLauncher(unit.Command) {
			continue
		}
		c.add(RuleUnitThroughLauncher, "units."+string(name)+".command",
			"start the unit's own binary as the first word of its command, without a shell or launcher in front of it",
			"unit %q starts through a shell or launcher, so what it runs is named in an argument this check cannot read; it can be shown neither to differ from the supervisor's executable nor to lie outside the directories other units write", name)
	}
}

package conformance

import (
	"path"
	"strings"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

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

// checkStandaloneBinaries closes what a list of launchers leaves open: no
// such list is complete, since any interpreter (python3 -c, perl -e,
// node -e) runs a program its arguments name. In Standalone mode, where
// every unit is a process of the one install, a unit's command must start a
// factory binary from the install's bin/ directory, which only `sisf up`
// writes; anything else is refused under the launcher rule. It runs after
// every other rule and passes over a unit whose command or binary another
// rule already refused, so one defect is reported once, by the rule that
// names it best; the manifest is refused either way.
func (c *checker) checkStandaloneBinaries() {
	if c.m.Mode != manifest.ModeStandalone {
		return
	}
	for _, name := range manifest.UnitNames() {
		unit, ok := c.m.Units[name]
		if !ok || name == manifest.UnitSupervisor || len(unit.Command) == 0 || factoryBinary(unit.Command[0]) {
			continue
		}
		at := "units." + string(name)
		if c.reportedAtUnit(at+".command") || c.reportedAtUnit(at+".image") {
			continue
		}
		c.add(RuleUnitThroughLauncher, at+".command",
			"start a factory binary from the install's bin/ directory, such as bin/factoryd, as the first word of the command",
			"unit %q starts %q, which is not a factory binary in the install's bin/ directory; a program from elsewhere, such as an interpreter, can run anything its arguments name, including the supervisor's executable", name, unit.Command[0])
	}
}

// reportedAtUnit reports whether any rule already failed at path or at a
// place below it, such as one word of a command.
func (c *checker) reportedAtUnit(path string) bool {
	path = withheldText(path)
	for _, f := range c.findings {
		if f.Path == path || strings.HasPrefix(f.Path, path+"[") || strings.HasPrefix(f.Path, path+".") {
			return true
		}
	}
	return false
}

// installBin is the directory of a Standalone install that holds the
// factory binaries `sisf up` builds, relative to the install's root.
const installBin = "bin"

// factoryBinary reports whether the first word of a Standalone command is a
// binary directly in the install's bin/ directory, written relative to the
// install as every unit `sisf up` defines is, with no dot segment. Case is
// folded, as the file system of the one machine may ignore it.
func factoryBinary(word string) bool {
	if word == "" || path.IsAbs(word) || strings.Contains(word, "\\") || path.Clean(word) != word {
		return false
	}
	dir, file := path.Split(word)
	return foldCase(dir) == foldCase(installBin+"/") && file != ""
}

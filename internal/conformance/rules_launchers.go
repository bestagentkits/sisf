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
//
// A unit compared by what it starts, the supervisor included, is refused
// too when the first word of its command names a file with whitespace
// around it: a file system may drop that whitespace and so run another
// program than the one every rule compares, such as a launcher that is not
// recognized by its padded name.
func (c *checker) checkLaunchers() {
	for _, name := range manifest.UnitNames() {
		unit, ok := c.m.Units[name]
		if !ok || len(unit.Command) == 0 || !c.comparedByExecutable(unit) {
			continue
		}
		if paddedFileName(unit.Command[0]) {
			c.add(RuleUnitThroughLauncher, "units."+string(name)+".command",
				"name the program the unit starts without whitespace around its file name",
				"unit %q starts %q, whose file name has whitespace around it; a file system may drop it and run another program than the one the rules compare", name, unit.Command[0])
			continue
		}
		if name == manifest.UnitSupervisor || !throughLauncher(unit.Command) {
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
		if !ok || name == manifest.UnitSupervisor || len(unit.Command) == 0 {
			continue
		}
		at := "units." + string(name)
		if c.reportedAtUnit(at+".command") || c.reportedAtUnit(at+".image") {
			continue
		}
		if !factoryBinary(unit.Command[0]) {
			c.add(RuleUnitThroughLauncher, at+".command",
				"start a factory binary from the install's bin/ directory, such as bin/factoryd, as the first word of the command",
				"unit %q starts %q, which is not a factory binary in the install's bin/ directory; a program from elsewhere, such as an interpreter, can run anything its arguments name, including the supervisor's executable", name, unit.Command[0])
			continue
		}
		c.checkOwnBinary(name, unit.Command)
	}
}

// unitBinaries is the binary in the install's bin/ directory that each unit
// of a Standalone install starts, as `sisf up` builds and defines them. The
// supervisor's binary has a rule of its own, which pins it; the application
// has none here, since it is the operator's program (see checkOwnBinary).
var unitBinaries = map[manifest.UnitName]string{
	manifest.UnitControl:    "factoryd",
	manifest.UnitDispatcher: "factoryd",
	manifest.UnitEdge:       "sisf-edge",
	manifest.UnitRunner:     "sisf-runner",
	manifest.UnitQARunner:   "sisf-qa-runner",
}

// appLauncherBinary is the launcher `sisf up` writes to bin/ for the
// supervisor's local deployments. It runs the program its second argument
// names, so it is a launcher like a shell: only the application unit may
// start it, and only without arguments, since the supervisor alone names
// that program, when it deploys a release.
const appLauncherBinary = "sisf-app-launcher"

// checkOwnBinary refuses a unit that starts a binary of bin/ other than its
// own. Every binary there is one `sisf up` builds or writes, but they are
// not interchangeable: the application launcher runs any program it is
// given, and the application's binary is not the factory. A factory unit
// starts its own binary. The application unit starts the application's
// program, any binary of bin/ the factory does not own, or the application
// launcher without arguments. Any other unit, such as state or artifacts,
// has no binary in bin/ and may start none of them.
func (c *checker) checkOwnBinary(name manifest.UnitName, command []string) {
	at := "units." + string(name) + ".command"
	file := path.Base(command[0])
	// A file system that ignores case runs bin/SISF-APP-LAUNCHER for
	// bin/sisf-app-launcher, so the application's names are folded; a
	// factory unit must name its binary exactly.
	launcher := foldCase(file) == foldCase(appLauncherBinary)
	if own, ok := unitBinaries[name]; ok {
		if file != own {
			c.add(RuleUnitThroughLauncher, at,
				"start the unit's own binary, bin/"+own+", as the first word of the command",
				"unit %q starts %q, not its own binary bin/%s; another binary of the install, such as the application launcher, runs what its arguments name, or is another unit's program", name, command[0], own)
		}
		return
	}
	if name != manifest.UnitApp {
		c.add(RuleUnitThroughLauncher, at,
			"remove the command; `sisf up` starts no program of the install's bin/ directory as unit "+string(name),
			"unit %q starts %q, a program of the install's bin/ directory, though it has no binary of its own there; the application launcher would run what its arguments name, and any other program there is another unit's", name, command[0])
		return
	}
	switch {
	case launcher && len(command) > 1:
		c.add(RuleUnitThroughLauncher, at,
			"start bin/"+appLauncherBinary+" without arguments; the supervisor gives it the program of a release when it deploys one",
			"unit %q passes arguments to the application launcher, which runs the program its second argument names; the manifest may not name that program", name)
	case !launcher && c.factoryOwned(file):
		c.add(RuleUnitThroughLauncher, at,
			"start the application's own program from bin/, or bin/"+appLauncherBinary+" without arguments",
			"unit %q starts %q, a binary of the factory, not the application's program", name, command[0])
	}
}

// supervisorBinary is the name `sisf up` gives the supervisor's binary.
const supervisorBinary = "sisf-supervisor"

// factoryOwned reports whether file is the binary of a factory unit: of
// the supervisor, by the name `sisf up` gives it whatever the manifest
// pins and as the manifest pins it, or of another factory unit.
func (c *checker) factoryOwned(file string) bool {
	if foldCase(file) == foldCase(supervisorBinary) {
		return true
	}
	if c.m.Supervisor != nil && c.m.Supervisor.Image != "" && foldCase(file) == foldCase(path.Base(c.m.Supervisor.Image)) {
		return true
	}
	for _, own := range unitBinaries {
		if foldCase(own) == foldCase(file) {
			return true
		}
	}
	return false
}

// paddedFileName reports whether the file named by word, the last element
// of its path, differs from itself with surrounding whitespace trimmed.
func paddedFileName(word string) bool {
	file := path.Base(word)
	return strings.TrimSpace(file) != file
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
// install as every unit `sisf up` defines is, with no dot segment. The
// directory is matched exactly: on a file system that heeds case, BIN/ is
// another directory, which `sisf up` does not write.
func factoryBinary(word string) bool {
	if word == "" || path.IsAbs(word) || strings.Contains(word, "\\") || path.Clean(word) != word {
		return false
	}
	dir, file := path.Split(word)
	return dir == installBin+"/" && file != ""
}

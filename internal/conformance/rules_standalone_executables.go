package conformance

import (
	"path"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// startedFile is a binary a unit starts on the one machine of a Standalone
// deployment, with the manifest place that names it.
type startedFile struct {
	at   string
	file string
}

// startedFiles lists the binaries a unit starts in Standalone mode: the
// first word of its command, which is what the runtime executes whatever
// its name, and the binary it names as its image.
func startedFiles(name manifest.UnitName, unit manifest.Unit) []startedFile {
	at := "units." + string(name)
	var files []startedFile
	if len(unit.Command) > 0 && unit.Command[0] != "" {
		files = append(files, startedFile{at: at + ".command", file: unit.Command[0]})
	}
	if unit.Image != "" {
		files = append(files, startedFile{at: at + ".image", file: unit.Image})
	}
	return files
}

// mountHolding returns a mount, among those keep accepts, whose directory
// is or holds file. A mount that climbs out of the factory root is left out:
// it is refused on its own, and every relative file would lie below it.
func (c *checker) mountHolding(file string, keep func(placedMount) bool) (placedMount, bool) {
	file = onDisk(file)
	for _, mount := range c.placedMounts() {
		if !climbs(mount.dir) && keep(mount) && within(file, mount.dir) {
			return mount, true
		}
	}
	return placedMount{}, false
}

// checkSupervisorExecutable applies to Standalone mode, where the
// supervisor's pinned image is a binary on the machine and the first word of
// its command is what the runtime starts. That word must be the pinned
// binary, so that the digest describes what runs, and no binary the
// supervisor starts may lie in a directory any unit mounts, because whoever
// writes that directory would replace the supervisor.
func (c *checker) checkSupervisorExecutable(sup manifest.Supervisor) {
	if c.m.Mode != manifest.ModeStandalone {
		return
	}
	own, declared := c.m.Units[manifest.UnitSupervisor]
	files := []startedFile{{at: "supervisor.image", file: sup.Image}}
	if declared {
		files = append(files, startedFiles(manifest.UnitSupervisor, own)...)
		if len(own.Command) > 0 && own.Command[0] != "" && sup.Image != "" && !throughLauncher(own.Command) &&
			path.Clean(own.Command[0]) != path.Clean(sup.Image) {
			c.addOnce(RuleSupervisorNotPinned, "units.supervisor.command",
				"make the first word of units.supervisor.command the pinned binary that supervisor.image names",
				"the supervisor unit starts a binary other than the pinned supervisor.image, so the pinned digest says nothing about what runs as the supervisor")
		}
	}
	for _, started := range files {
		if started.file == "" {
			continue
		}
		if mount, found := c.mountHolding(started.file, func(placedMount) bool { return true }); found {
			c.addOnce(RuleSupervisorNotPinned, started.at,
				"install the supervisor's binary in a directory no unit mounts, such as the factory's bin directory",
				"the supervisor's binary lies inside the directory of volume %q, which unit %q mounts (%s); whoever writes that directory replaces the supervisor", mount.volume, mount.unit, mount.at)
		}
	}
}

// checkStandaloneExecutables applies to Standalone mode: no unit other than
// the supervisor, which has a stricter rule of its own, starts a binary from
// a directory another unit mounts, because that unit could then rewrite
// what runs, across zones included.
func (c *checker) checkStandaloneExecutables() {
	if c.m.Mode != manifest.ModeStandalone {
		return
	}
	for _, name := range manifest.UnitNames() {
		unit, ok := c.m.Units[name]
		if !ok || name == manifest.UnitSupervisor {
			continue
		}
		for _, started := range startedFiles(name, unit) {
			mount, found := c.mountHolding(started.file, func(mount placedMount) bool { return mount.unit != name })
			if !found {
				continue
			}
			c.addOnce(RuleExecutableInAnotherUnitsDirectory, started.at,
				"install the unit's binary in a directory no other unit mounts, such as the factory's bin directory",
				"unit %q starts a binary inside the directory of volume %q, which unit %q mounts (%s); on one machine that unit rewrites what %q runs", name, mount.volume, mount.unit, mount.at, name)
		}
	}
}

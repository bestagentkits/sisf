package conformance

import (
	"fmt"
	"path"
	"strings"
	"unicode"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// startedFile is a file a unit starts or names in its command on the one
// machine of a Standalone deployment, with the manifest place that names it.
type startedFile struct {
	at   string
	file string
	// argument marks a later word of the command, which names a file the
	// program may load or run rather than the program itself.
	argument bool
}

// startedFiles lists what a unit runs in Standalone mode: the first word of
// its command, which is what the runtime executes whatever its name, the
// binary it names as its image, and every file a later word of the command
// names (see namedFiles), because an interpreter, a loader or a wrapper runs
// the file it is given. A URL names no file of the machine and is left out;
// a file URL names the file after its scheme.
func startedFiles(name manifest.UnitName, unit manifest.Unit) []startedFile {
	at := "units." + string(name)
	var files []startedFile
	if len(unit.Command) > 0 && unit.Command[0] != "" {
		files = append(files, startedFile{at: at + ".command", file: unit.Command[0]})
	}
	if unit.Image != "" {
		files = append(files, startedFile{at: at + ".image", file: unit.Image})
	}
	for i, word := range unit.Command {
		if i == 0 {
			continue
		}
		place := fmt.Sprintf("%s.command[%d]", at, i)
		for _, file := range namedFiles(word) {
			files = append(files, startedFile{at: place, file: file, argument: true})
		}
	}
	return files
}

// namedFiles lists the files of the machine one word of a command may name:
// each part of it between spaces and shell punctuation, as in the script a
// shell is given with -c, and the value after "=" in each part, as in
// --exec=FILE.
func namedFiles(word string) []string {
	var files []string
	for _, part := range strings.FieldsFunc(word, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(";&|()<>`'\"", r)
	}) {
		candidates := []string{part}
		if _, value, assigned := strings.Cut(part, "="); assigned {
			candidates = append(candidates, value)
		}
		for _, candidate := range candidates {
			if file, local := localFile(candidate); local {
				files = append(files, file)
			}
		}
	}
	return files
}

// localFile returns the file of the machine a word of a command may name:
// the word itself, or the path of a file URL. A URL of any other scheme and
// an empty word name none.
func localFile(word string) (string, bool) {
	if rest, found := strings.CutPrefix(strings.ToLower(word), "file://"); found {
		return word[len(word)-len(rest):], rest != ""
	}
	return word, word != "" && !strings.Contains(word, "://")
}

// climbingFileFix says why a path that climbs out of the factory root is
// refused even when it leads to a sibling directory.
const climbingFileFix = "name a file inside the factory root, without .. climbing above it, or by an absolute path; " +
	"a path that climbs out, even to a sibling such as ../app-data, is refused only because the name of the factory root is unknown, so it may lead back into another unit's directory"

// mountHolding returns a mount, among those keep accepts, whose directory
// is or holds file. A mount that climbs out of the factory root is left out:
// it is refused on its own, and every relative file would lie below it. An
// absolute file is taken to lie in a relative directory when its path runs
// through that directory, as /home/owner/factory/data/workspaces/x runs
// through data/workspaces, because the factory root may be any directory.
func (c *checker) mountHolding(file string, keep func(placedMount) bool) (placedMount, bool) {
	file = onDisk(file)
	for _, mount := range c.placedMounts() {
		if climbs(mount.dir) || !keep(mount) {
			continue
		}
		if within(file, mount.dir) || runsThrough(file, mount.dir) {
			return mount, true
		}
	}
	return placedMount{}, false
}

// runsThrough reports whether an absolute file lies, below some directory,
// in the relative directory dir: whether dir is a run of whole elements of
// the file's path.
func runsThrough(file, dir string) bool {
	if !path.IsAbs(file) || path.IsAbs(dir) || dir == "." || climbs(dir) {
		return false
	}
	return strings.Contains(file+"/", "/"+dir+"/")
}

// checkSupervisorExecutable applies to Standalone mode, where the
// supervisor's pinned image is a binary on the machine and the first word of
// its command is what the runtime starts. That word must be the pinned
// binary, so that the digest describes what runs, and no binary the
// supervisor starts may lie in a directory any unit mounts, because whoever
// writes that directory would replace the supervisor. A later word of its
// command, which may name a file the supervisor loads, may not lie in a
// directory another unit mounts, the trust volume aside.
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
		if climbs(started.file) {
			c.addOnce(RuleSupervisorNotPinned, started.at, climbingFileFix,
				"the supervisor's command names a file through a path that climbs out of the factory root, so it may lie in a directory a unit mounts and whoever writes that directory would change what the supervisor runs")
			continue
		}
		// A later argument may name the supervisor's own directories and
		// the trust volume, which no other unit may write.
		keep := func(placedMount) bool { return true }
		if started.argument {
			keep = func(mount placedMount) bool {
				return mount.unit != manifest.UnitSupervisor && mount.volume != sup.TrustVolume
			}
		}
		if mount, found := c.mountHolding(started.file, keep); found {
			c.addOnce(RuleSupervisorNotPinned, started.at,
				"install the supervisor's binary, and every file its command names, in a directory no other unit mounts, such as the factory's bin directory",
				"the supervisor's command or binary names a file inside the directory of volume %q, which unit %q mounts (%s); whoever writes that directory replaces what the supervisor runs", mount.volume, mount.unit, mount.at)
		}
	}
}

// checkStandaloneExecutables applies to Standalone mode: no unit other than
// the supervisor, which has a stricter rule of its own, starts a binary from
// a directory another unit mounts, or names a file there in its command for
// an interpreter or a loader to run, because that unit could then rewrite
// what runs, across zones included. A path that climbs out of the factory
// root is refused, since it may lead back into any directory below it.
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
			if climbs(started.file) {
				c.addOnce(RuleExecutableInAnotherUnitsDirectory, started.at, climbingFileFix,
					"unit %q starts or names a file through a path that climbs out of the factory root; on one machine that path may lie in another unit's directory, and that unit would then rewrite what %q runs", name, name)
				continue
			}
			mount, found := c.mountHolding(started.file, func(mount placedMount) bool { return mount.unit != name })
			if !found {
				continue
			}
			c.addOnce(RuleExecutableInAnotherUnitsDirectory, started.at,
				"install the unit's binary, and every file its command names, in a directory no other unit mounts, such as the factory's bin directory",
				"unit %q starts or names in its command a file inside the directory of volume %q, which unit %q mounts (%s); on one machine that unit rewrites what %q runs", name, mount.volume, mount.unit, mount.at, name)
		}
	}
}

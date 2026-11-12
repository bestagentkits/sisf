package conformance

import (
	"path"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// imageRepository returns an image reference without its tag and digest, so
// that two builds of one repository are recognized as the same artifact line.
func imageRepository(image string) string {
	image, _, _ = strings.Cut(image, "@")
	if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
		image = image[:colon]
	}
	return image
}

// dockerHubHosts are the names Docker Hub answers to.
var dockerHubHosts = []string{"docker.io", "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com"}

// canonicalRepository returns the repository of an image in the one form
// all its spellings share: no transport prefix, no tag or digest, no empty
// path segment, lower case, and Docker Hub written out. "acme/x",
// "docker.io/acme/x:v2", "index.docker.io/acme/x/" and
// "docker://docker.io/acme/x" are then one repository.
func canonicalRepository(image string) string {
	image = strings.ToLower(strings.TrimSpace(image))
	if _, rest, found := strings.Cut(image, "://"); found {
		image = rest
	}
	segments := strings.FieldsFunc(image, func(r rune) bool { return r == '/' })
	if len(segments) == 0 {
		return ""
	}
	segments = strings.Split(imageRepository(strings.Join(segments, "/")), "/")
	// The first segment is a registry when it looks like a host; a bare name
	// such as "acme" is an account on Docker Hub.
	registry := "docker.io"
	if first := segments[0]; len(segments) > 1 && (strings.ContainsAny(first, ".:") || first == "localhost") {
		registry, segments = first, segments[1:]
	}
	if slices.Contains(dockerHubHosts, registry) {
		registry = "docker.io"
		if len(segments) == 1 {
			segments = []string{"library", segments[0]} // official images live under library/
		}
	}
	return registry + "/" + strings.Join(segments, "/")
}

// launchers are commands that start another program named in their
// arguments. The first word of such a command does not say which executable
// runs, so it can be neither matched to a pinned artifact nor told apart
// from the executable of another unit.
var launchers = []string{"sh", "bash", "dash", "zsh", "ash", "ksh", "csh", "tcsh", "fish", "busybox",
	"env", "exec", "nohup", "sudo", "doas", "setsid", "nice", "timeout", "xargs"}

// executableName returns the name an executable is recognized by wherever
// it is installed: the last element of its cleaned path.
func executableName(file string) string { return path.Base(path.Clean(file)) }

// throughLauncher reports whether a command starts through a shell or
// another launcher. Letter case is folded in every mode: a file system that
// ignores case runs /bin/sh for /bin/SH.
func throughLauncher(command []string) bool {
	if len(command) == 0 {
		return false
	}
	name := foldCase(executableName(command[0]))
	return slices.ContainsFunc(launchers, func(l string) bool { return foldCase(l) == name })
}

// comparedByExecutable reports whether what a unit starts identifies its
// artifact. It does where no image stands for the unit: in Standalone mode,
// where an image is a binary on the one machine, and for a unit that names
// no image at all. Two units of a Docker deployment that run different
// images are different artifacts even when both start a launcher of the same
// name, such as an init wrapper or an interpreter.
func (c *checker) comparedByExecutable(unit manifest.Unit) bool {
	return c.m.Mode == manifest.ModeStandalone || unit.Image == ""
}

// startedExecutables lists the paths of what a unit starts, as written:
// the first word of its command and, in Standalone mode, the binary it names
// as its image. A command that starts through a launcher names nothing.
func (c *checker) startedExecutables(unit manifest.Unit, images ...string) []string {
	var started []string
	if len(unit.Command) > 0 && unit.Command[0] != "" && !throughLauncher(unit.Command) {
		started = append(started, unit.Command[0])
	}
	if c.m.Mode == manifest.ModeStandalone {
		for _, image := range append(images, unit.Image) {
			if image != "" {
				started = append(started, image)
			}
		}
	}
	return started
}

// executableNames returns the names the executables are recognized by
// wherever they are installed.
func (c *checker) executableNames(executables []string) []string {
	names := make([]string, 0, len(executables))
	for _, file := range executables {
		names = append(names, c.spelled(executableName(file)))
	}
	return names
}

// spelled returns a path or a name in the form two spellings of one file
// share: on the one machine of a Standalone deployment the file system may
// ignore letter case, so case is folded there; inside a container it is not.
func (c *checker) spelled(file string) string {
	if c.m.Mode == manifest.ModeStandalone {
		return foldCase(file)
	}
	return file
}

// namesExecutable reports whether a command names one of the executables.
// Its first word is what runs, so it names an executable of the same name
// from any directory. A wrapper such as an init process, an interpreter or
// a shell runs the file it is given in a later word (see namedFiles), so a
// later word names an executable when it is the bare
// name, found through the search path, or the path the executable is
// started from, written any way that cleans to it or under a directory the
// manifest does not name (/opt/factory/bin/x is bin/x of a factory rooted
// there); an executable started by its bare name may lie in any directory. A word that merely ends in the name, such as logs/sisf-supervisor
// or a URL, names another file.
func (c *checker) namesExecutable(command, executables []string) bool {
	names := c.executableNames(executables)
	paths := make([]string, 0, len(executables))
	for _, file := range executables {
		paths = append(paths, c.spelled(path.Clean(file)))
	}
	for i, word := range command {
		for _, file := range namedFiles(word) {
			if i == 0 {
				if slices.Contains(names, c.spelled(executableName(file))) {
					return true
				}
				continue
			}
			if !strings.Contains(file, "/") {
				if slices.Contains(names, c.spelled(file)) {
					return true
				}
				continue
			}
			// A path started by its bare name is found through the search
			// path, so any directory may hold it.
			if slices.ContainsFunc(paths, func(started string) bool {
				return samePlace(c.spelled(path.Clean(file)), started) ||
					(!strings.Contains(started, "/") && c.spelled(executableName(file)) == started)
			}) {
				return true
			}
		}
	}
	return false
}

// samePlace reports whether two cleaned paths may name one file: they are
// equal, or one is relative to a directory the manifest does not name and
// the other leads through that directory to it. A bare name is matched as a
// name, not here.
func samePlace(a, b string) bool {
	if a == b {
		return true
	}
	suffix := func(long, short string) bool {
		return strings.Contains(short, "/") && !path.IsAbs(short) && !climbs(short) && strings.HasSuffix(long, "/"+short)
	}
	return suffix(a, b) || suffix(b, a)
}

// checkSupervisorArtifact verifies condition 1. The supervisor runs from a
// digest-pinned artifact of its own, and no other unit shares it. Another
// unit shares the supervisor's artifact when it has the supervisor's digest,
// when its image is the supervisor's repository under any tag, digest or
// registry alias, or, where executables identify artifacts, when it starts
// an executable of the supervisor's name from any directory or names the
// supervisor's executable in a later argument (see namesExecutable). A unit
// that starts through a shell hides what it runs and is refused for that by
// checkLaunchers.
// In Standalone mode the supervisor must also start the binary it pins,
// from a directory no unit mounts.
func (c *checker) checkSupervisorArtifact(sup manifest.Supervisor) {
	if err := sup.Digest.Validate(); err != nil {
		c.add(RuleSupervisorNotPinned, "supervisor.digest",
			"set supervisor.digest to the sha256 or sha512 digest of the supervisor's artifact",
			"supervisor.digest %q is not a full content digest; a tag or a name can be moved to another artifact", string(sup.Digest))
	}
	if sup.Image == "" {
		c.add(RuleSupervisorNotPinned, "supervisor.image",
			"set supervisor.image to the supervisor's own image or binary",
			"the supervisor names no artifact of its own to run from")
	}
	own, declared := c.m.Units[manifest.UnitSupervisor]
	if declared && own.Digest != "" && own.Digest != sup.Digest {
		c.add(RuleSupervisorNotPinned, "units.supervisor.digest",
			"make units.supervisor.digest equal supervisor.digest, or leave it out",
			"the supervisor unit runs digest %q, not the pinned %q", string(own.Digest), string(sup.Digest))
	}
	if declared && own.Image != "" && own.Image != sup.Image {
		c.add(RuleSupervisorNotPinned, "units.supervisor.image",
			"make units.supervisor.image equal supervisor.image, or leave it out",
			"the supervisor unit runs %q, not the pinned %q", own.Image, sup.Image)
	}
	// The supervisor's own image stands for it even when the unit repeats none.
	pinned := own
	if sup.Image != "" {
		pinned.Image = sup.Image
	}
	if c.comparedByExecutable(pinned) && throughLauncher(own.Command) {
		c.add(RuleSupervisorNotPinned, "units.supervisor.command",
			"start the supervisor's own binary as the first word of units.supervisor.command, without a shell or launcher in front of it",
			"the supervisor unit starts through a shell or launcher, so the executable that runs as the supervisor is named in an argument this check cannot read; it can be shown neither to be the pinned artifact nor to differ from what another unit runs")
	}
	c.checkSupervisorExecutable(sup)
	c.checkSupervisorArtifactShared(sup, c.startedExecutables(own, sup.Image))
}

// checkSupervisorArtifactShared compares every other unit with the
// supervisor's digest, repository and executables, given as the paths the
// supervisor starts them from.
func (c *checker) checkSupervisorArtifactShared(sup manifest.Supervisor, executables []string) {
	const fix = "build the supervisor as its own artifact, separate from every unit a release can replace"
	repository := canonicalRepository(sup.Image)
	for _, name := range manifest.UnitNames() {
		unit, ok := c.m.Units[name]
		if !ok || name == manifest.UnitSupervisor {
			continue
		}
		at := "units." + string(name)
		switch {
		case unit.Digest != "" && unit.Digest == sup.Digest:
			c.add(RuleSupervisorSharesArtifact, at+".digest", fix,
				"unit %q has the supervisor's digest, so the supervisor is built from the same artifact as a candidate", name)
		case unit.Image != "" && repository != "" && canonicalRepository(unit.Image) == repository:
			c.add(RuleSupervisorSharesArtifact, at+".image", fix,
				"unit %q runs image %q, which is the supervisor's repository %q whatever its tag, digest or registry alias, so a candidate built there is built where the supervisor is", name, unit.Image, imageRepository(sup.Image))
		case !c.comparedByExecutable(unit):
			// Its image, already compared, is what identifies this unit.
		case c.namesExecutable(unit.Command, executables):
			c.add(RuleSupervisorSharesArtifact, at+".command", fix,
				"unit %q names the executable the supervisor starts in its command, so whatever program comes first can run the supervisor's executable, and replacing that unit replaces the supervisor", name)
		case c.m.Mode == manifest.ModeStandalone && unit.Image != "" && slices.Contains(c.executableNames(executables), c.spelled(executableName(unit.Image))):
			c.add(RuleSupervisorSharesArtifact, at+".image", fix,
				"unit %q names as its binary the executable the supervisor starts, under another directory, so replacing that unit replaces the supervisor", name)
		}
	}
}

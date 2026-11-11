package conformance

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// placedMount is one mount of a declared volume, seen as the directory it
// names on the machine.
type placedMount struct {
	unit   manifest.UnitName
	at     string // manifest path of the mount
	volume string
	dir    string // cleaned mount path
	// owner tells volumes with different owners apart: the owning zone, and
	// for a shared volume also the unit that holds it.
	owner string
}

// placedMounts lists the mounts of declared volumes in unit order.
func (c *checker) placedMounts() []placedMount {
	var placed []placedMount
	for _, name := range manifest.UnitNames() {
		for i, mount := range c.m.Units[name].Mounts {
			volume, declared := c.m.Volumes[mount.Volume]
			if !declared || mount.Path == "" {
				continue
			}
			owner := string(volume.Zone)
			if volume.Zone.Shared() {
				owner += "/" + string(c.sharedVolumeHolder(mount.Volume))
			}
			placed = append(placed, placedMount{
				unit: name, at: fmt.Sprintf("units.%s.mounts[%d]", name, i),
				volume: mount.Volume, dir: path.Clean(mount.Path), owner: owner,
			})
		}
	}
	return placed
}

// checkStandalonePaths applies to Standalone mode, where every unit is a
// process on one machine and a mount is a directory there. Two volumes
// whose directories are equal, or one inside the other, are one place on
// disk whatever their names say, so:
//
//   - no volume shares a directory with the supervisor's trust volume,
//     because whoever writes that volume would reach the last known good
//     release and the signing keys;
//   - no two volumes with different owners share a directory, because one
//     zone would then write the other's files;
//   - no mount climbs out of the factory root, because a directory above it
//     holds every directory below it, the trust volume and the other zones'
//     data included (see refuseClimbingMount).
//
// Directories are compared as written, after cleaning; in Docker mode a
// mount path is a place inside one container and says nothing of another.
func (c *checker) checkStandalonePaths() {
	if c.m.Mode != manifest.ModeStandalone {
		return
	}
	trust := ""
	if c.m.Supervisor != nil {
		trust = c.m.Supervisor.TrustVolume
	}
	placed := c.placedMounts()
	var mounts []placedMount
	for _, mount := range placed {
		if climbs(mount.dir) {
			c.refuseClimbingMount(mount, trust, placed)
			continue
		}
		mounts = append(mounts, mount)
	}
	for i, later := range mounts {
		for _, earlier := range mounts[:i] {
			if later.volume == earlier.volume || !overlap(later.dir, earlier.dir) {
				continue
			}
			switch {
			case trust != "" && (later.volume == trust || earlier.volume == trust):
				offender, held := later, earlier
				if later.volume == trust {
					offender, held = earlier, later
				}
				c.addOnce(RuleTrustVolumeExposed, offender.at+".path",
					fmt.Sprintf("mount volume %q at a directory outside the one %s names, and not around it", offender.volume, held.at),
					"unit %q mounts volume %q at a directory that is, holds or lies inside the directory of trust volume %q (%s); on one machine they are the same files, so whoever writes that volume reaches the last known good release and the signing keys", offender.unit, offender.volume, trust, held.at)
			case later.owner != earlier.owner:
				c.addOnce(RuleVolumePathOverlap, later.at+".path",
					fmt.Sprintf("mount volume %q at a directory outside the one %s names, and not around it", later.volume, earlier.at),
					"unit %q mounts volume %q at a directory that is, holds or lies inside the directory of volume %q (%s), which has another owner; on one machine they are the same files, so one owner writes the other's data", later.unit, later.volume, earlier.volume, earlier.at)
			}
		}
	}
}

// refuseClimbingMount reports a mount whose directory climbs out of the
// factory root or is the root of the file system. Such a directory holds
// every relative directory of the deployment, so it is reported once, as an
// exposure of the trust volume when it is or holds that volume's directory
// and as an overlap of owners otherwise, and is not compared pair by pair.
func (c *checker) refuseClimbingMount(mount placedMount, trust string, placed []placedMount) {
	const fix = "mount the volume at a directory inside the factory root, without .. climbing above it, or at an absolute directory of its own other than the root of the file system"
	holdsTrust := trust != "" && (mount.volume == trust || slices.ContainsFunc(placed, func(other placedMount) bool {
		return other.volume == trust && within(other.dir, mount.dir)
	}))
	if holdsTrust {
		c.addOnce(RuleTrustVolumeExposed, mount.at+".path", fix,
			"unit %q mounts volume %q at a directory that climbs out of the factory root; on one machine that directory holds the trust volume %q and every other directory of the deployment, so whoever writes one of them reaches the last known good release and the signing keys", mount.unit, mount.volume, trust)
		return
	}
	c.addOnce(RuleVolumePathOverlap, mount.at+".path", fix,
		"unit %q mounts volume %q at a directory that climbs out of the factory root; on one machine that directory holds every other directory of the deployment, so its owner writes the data of the other owners", mount.unit, mount.volume)
}

// overlap reports whether two cleaned directories are equal or nested.
func overlap(a, b string) bool { return within(a, b) || within(b, a) }

// climbs reports whether a path leads out of the factory root once cleaned:
// a relative path that climbs out of the directory it is relative to, such
// as ".." or "../elsewhere", or the root of the file system, which holds the
// factory root wherever it is.
func climbs(file string) bool {
	file = path.Clean(file)
	return file == ".." || strings.HasPrefix(file, "../") || file == "/"
}

// within reports whether location is root or a path below it.
func within(location, root string) bool {
	location, root = path.Clean(location), path.Clean(root)
	switch {
	case root == "/":
		// The root of the file system holds every path, relative or not.
		return true
	case climbs(root):
		// A directory above the working directory holds the working
		// directory, so it is taken to hold every relative path; how far a
		// relative location climbs cannot shrink what such a root may hold.
		return !path.IsAbs(location)
	case root == ".":
		// The working directory holds every relative path that does not climb out of it.
		return !path.IsAbs(location) && !climbs(location)
	}
	return location == root || strings.HasPrefix(location, strings.TrimSuffix(root, "/")+"/")
}

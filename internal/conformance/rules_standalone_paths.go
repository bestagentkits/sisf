package conformance

import (
	"fmt"
	"path"
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
//     zone would then write the other's files.
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
	mounts := c.placedMounts()
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

// overlap reports whether two cleaned directories are equal or nested.
func overlap(a, b string) bool { return within(a, b) || within(b, a) }

// within reports whether location is root or a path below it.
func within(location, root string) bool {
	location, root = path.Clean(location), path.Clean(root)
	if root == "." {
		// The working directory holds every relative path that does not climb out of it.
		return !path.IsAbs(location) && location != ".." && !strings.HasPrefix(location, "../")
	}
	return location == root || strings.HasPrefix(location, strings.TrimSuffix(root, "/")+"/")
}

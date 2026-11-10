package conformance

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkTrustVolume verifies condition 2.
func (c *checker) checkTrustVolume(sup manifest.Supervisor) {
	volume, declared := c.m.Volumes[sup.TrustVolume]
	if sup.TrustVolume == "" || !declared {
		c.add(RuleTrustVolumeUndeclared, "supervisor.trust_volume",
			"declare the volume under volumes with zone: control and name it in supervisor.trust_volume",
			"trust volume %q is not declared, so nothing shows where the last known good release, the signing keys and the rollback configuration are kept", sup.TrustVolume)
		return
	}
	if volume.Zone != core.ZoneControl {
		c.add(RuleTrustVolumeExposed, fmt.Sprintf("volumes.%s.zone", sup.TrustVolume),
			fmt.Sprintf("set volumes.%s.zone to control", sup.TrustVolume),
			"trust volume %q is writable by %s; only Zone 3 may hold the last known good release, the signing keys and the rollback configuration", sup.TrustVolume, zoneLabel(volume.Zone))
	}
	held := false
	for _, name := range manifest.UnitNames() {
		for i, mount := range c.m.Units[name].Mounts {
			if mount.Volume != sup.TrustVolume {
				continue
			}
			at := fmt.Sprintf("units.%s.mounts[%d]", name, i)
			switch {
			case name == manifest.UnitSupervisor:
				held = held || !mount.ReadOnly
			case name.Zone() != core.ZoneControl:
				c.add(RuleTrustVolumeExposed, at,
					fmt.Sprintf("remove the mount of %q from units.%s", sup.TrustVolume, name),
					"unit %q of %s mounts the trust volume; no process outside Zone 3 and no agent workspace may reach the last known good release or the signing keys", name, zoneLabel(name.Zone()))
			case slices.Contains(c.m.Release.Units, name):
				c.add(RuleTrustVolumeExposed, at,
					fmt.Sprintf("remove the mount of %q from units.%s, or take %s out of release.units", sup.TrustVolume, name, name),
					"unit %q is in release.units, so the factory publishes it by itself and a candidate runs as it; a candidate may not read any key, so it does not mount the trust volume at all, read-only included", name)
			case !mount.ReadOnly:
				c.add(RuleTrustVolumeExposed, at,
					fmt.Sprintf("set read_only: true on this mount, or remove it from units.%s", name),
					"unit %q can write the trust volume; a release can replace that unit, so it must not be able to change the last known good release", name)
			}
		}
	}
	if _, ok := c.m.Units[manifest.UnitSupervisor]; ok && !held {
		c.add(RuleTrustVolumeUnmounted, "units.supervisor.mounts",
			fmt.Sprintf("mount volume %q writable in units.supervisor", sup.TrustVolume),
			"the supervisor unit does not mount trust volume %q writable, so what it must protect is kept somewhere this manifest does not guard", sup.TrustVolume)
	}
}

// checkTrustVolumeStorage verifies the part of condition 2 that keeps the
// trust volume for the supervisor's own records. The state store and the
// artifact store take what agents and candidates produce; neither may be
// kept on the trust volume, whether it is named outright or addressed by a
// path inside the directory a unit mounts it at.
func (c *checker) checkTrustVolumeStorage(sup manifest.Supervisor) {
	if sup.TrustVolume == "" {
		return
	}
	inside := []string{sup.TrustVolume}
	for _, name := range manifest.UnitNames() {
		for _, mount := range c.m.Units[name].Mounts {
			if mount.Volume == sup.TrustVolume && mount.Path != "" {
				inside = append(inside, mount.Path)
			}
		}
	}
	stores := []struct{ path, value, holds string }{
		{"storage.artifacts.location", c.m.Storage.Artifacts.Location, "artifacts, logs and snapshots"},
		{"storage.state.path", c.m.Storage.State.Path, "the state store"},
	}
	for _, store := range stores {
		if store.value == "" || !slices.ContainsFunc(inside, func(root string) bool { return within(store.value, root) }) {
			continue
		}
		c.add(RuleTrustVolumeStorage, store.path,
			fmt.Sprintf("set %s to a place outside trust volume %q", store.path, sup.TrustVolume),
			"%s places %s on trust volume %q; what agents and candidates produce may not sit beside the last known good release and the signing keys", store.path, store.holds, sup.TrustVolume)
	}
}

// within reports whether location is root or a path below it.
func within(location, root string) bool {
	location, root = path.Clean(location), path.Clean(root)
	return location == root || strings.HasPrefix(location, strings.TrimSuffix(root, "/")+"/")
}

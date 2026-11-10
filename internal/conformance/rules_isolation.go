package conformance

import (
	"fmt"
	"sort"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkIsolation verifies the boundaries between the three zones: each has
// its own credentials and its own store role and no secret stands in for
// another, no unit reaches a volume it has no claim to (so Zone 1 has no
// path into Zone 3), on one machine no two owners share a directory, every
// unit stays on the network segments its role allows, the sandbox reaches
// the outside only through its egress proxy, only the units meant to face
// the outside do, and a runner listens on nothing.
func (c *checker) checkIsolation() {
	c.checkStoreRoles()
	c.checkSecretReferences()
	c.checkVolumeHolders()
	c.checkVolumeMounts()
	c.checkStandalonePaths()
	c.checkSegments()
	c.checkEgress()
	c.checkPublicExposure()
	c.checkRunnerInbound()
}

// checkVolumeMounts judges every mount by who owns the volume:
//
//   - a volume of the unit's own zone may be mounted freely;
//   - a volume marked shared is the data of a unit every zone uses and is
//     mounted by that unit alone (see sharedVolumeHolder), read-only mounts
//     included;
//   - a volume of another zone is never mounted writable, and a unit of
//     Zone 1 or Zone 2 does not mount it at all: the application and the
//     agents reach another zone through its API, not through its files. A
//     Zone 3 unit, or one every zone uses, may mount it read-only.
//
// The supervisor's trust volume has stricter rules of its own and is left to
// them.
func (c *checker) checkVolumeMounts() {
	trust := ""
	if c.m.Supervisor != nil {
		trust = c.m.Supervisor.TrustVolume
	}
	for _, name := range manifest.UnitNames() {
		for i, mount := range c.m.Units[name].Mounts {
			path := fmt.Sprintf("units.%s.mounts[%d]", name, i)
			volume, declared := c.m.Volumes[mount.Volume]
			if !declared {
				c.add(RuleVolumeUndeclared, path+".volume",
					fmt.Sprintf("declare %q under volumes with the zone that owns it", mount.Volume),
					"unit %q mounts volume %q, which is not declared, so no zone owns it and its isolation cannot be judged", name, mount.Volume)
				continue
			}
			owner, zone := volume.Zone, name.Zone()
			remove := fmt.Sprintf("remove this mount from units.%s", name)
			switch {
			case mount.Volume == trust || !owner.Valid():
			case owner.Shared():
				if holder := c.sharedVolumeHolder(mount.Volume); name != holder {
					c.add(RuleSharedVolumeMount, path,
						fmt.Sprintf("%s and reach the data through the %s unit", remove, holder),
						"unit %q mounts volume %q, which is marked shared; only the %s unit mounts it, and every zone uses what it holds through that unit, never through a mount of its own", name, mount.Volume, holder)
				}
			case owner == zone:
			case !mount.ReadOnly:
				fix := "set read_only: true on this mount, or " + remove
				if confined(zone) {
					fix = remove
				}
				c.add(RuleCrossZoneWrite, path, fix,
					"unit %q of %s can write volume %q, which belongs to %s; a zone has no write path into another zone", name, zoneLabel(zone), mount.Volume, zoneLabel(owner))
			case confined(zone):
				c.add(RuleCrossZoneRead, path, remove,
					"unit %q of %s mounts volume %q, which belongs to %s; Zone 1 and Zone 2 reach another zone only through its API and cannot read its files, read-only included", name, zoneLabel(zone), mount.Volume, zoneLabel(owner))
			}
		}
	}
}

// confined reports whether units of zone z may mount only their own zone's
// volumes. That holds for Zone 1, which must not read the secrets of the
// other zones, and for Zone 2, which calls Zone 3 only through its API.
func confined(z core.Zone) bool { return z == core.ZoneApplication || z == core.ZoneAgent }

// sharedVolumeHolder returns the one unit that may mount a volume marked
// shared. A volume may name its holder, which is how the edge keeps its TLS
// state. One that names none is kept by a durable unit: the artifacts unit
// holds the volume storage.artifacts names, and the state unit holds any
// other. A holder that is not a unit every zone uses is refused by
// checkVolumeHolders and is not honoured here.
func (c *checker) sharedVolumeHolder(volume string) manifest.UnitName {
	if holder := c.m.Volumes[volume].Holder; holder.Valid() && holder.Zone().Shared() {
		return holder
	}
	if store := c.m.Storage.Artifacts; store.Kind == manifest.ArtifactsVolume && store.Location == volume {
		return manifest.UnitArtifacts
	}
	return manifest.UnitState
}

// checkVolumeHolders allows a holder only on a shared volume and only a
// unit every zone uses (state, artifacts or edge). A volume a zone owns is
// that zone's; naming a zone's unit as the holder of shared data would give
// one zone the files every zone relies on.
func (c *checker) checkVolumeHolders() {
	names := make([]string, 0, len(c.m.Volumes))
	for name := range c.m.Volumes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		volume := c.m.Volumes[name]
		if volume.Holder == "" {
			continue
		}
		path := fmt.Sprintf("volumes.%s.holder", name)
		switch {
		case !volume.Zone.Shared():
			c.add(RuleVolumeHolder, path,
				fmt.Sprintf("remove holder from volumes.%s, or set its zone to shared", name),
				"volume %q names a holder although %s owns it; only a volume marked shared is kept by one unit on behalf of every zone", name, zoneLabel(volume.Zone))
		case !volume.Holder.Valid() || !volume.Holder.Zone().Shared():
			c.add(RuleVolumeHolder, path,
				fmt.Sprintf("set volumes.%s.holder to state, artifacts or edge, or give the volume to the zone that uses it", name),
				"shared volume %q names a holder that is not one of the units every zone uses; a unit of one zone holding shared data would own what the other zones rely on", name)
		}
	}
}

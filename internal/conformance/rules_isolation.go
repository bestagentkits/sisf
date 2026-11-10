package conformance

import (
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkIsolation verifies the boundaries between the three zones: each has
// its own credentials and its own store role, no unit reaches a volume it
// has no claim to (so Zone 1 has no path into Zone 3), every unit stays on
// the network segments its role allows, the sandbox reaches the outside only
// through its egress proxy, and only the units meant to face the outside do.
func (c *checker) checkIsolation() {
	c.checkZoneSecrets()
	c.checkVolumeMounts()
	c.checkSegments()
	c.checkEgress()
	c.checkPublicExposure()
}

// checkZoneSecrets rejects a credential set or store role used by two zones.
func (c *checker) checkZoneSecrets() {
	zones := core.Zones()
	for i, later := range zones {
		component := c.m.Components.Zone(later)
		if component == nil {
			continue
		}
		for _, earlier := range zones[:i] {
			other := c.m.Components.Zone(earlier)
			if other == nil {
				continue
			}
			if component.CredentialsRef != "" && component.CredentialsRef == other.CredentialsRef {
				c.add(RuleSharedCredentials, fmt.Sprintf("components.%s.credentials_ref", later),
					"give each zone a credential set of its own",
					"%s and %s reference the same credential set, so a breach of one zone reaches the other", zoneLabel(earlier), zoneLabel(later))
			}
			if component.StoreRole != "" && component.StoreRole == other.StoreRole {
				c.add(RuleSharedStoreRole, fmt.Sprintf("components.%s.store_role", later),
					"give each zone a state-store role of its own",
					"%s and %s open the state store with the same role %q, so one zone can read and write the other's data", zoneLabel(earlier), zoneLabel(later), component.StoreRole)
			}
		}
	}
}

// checkVolumeMounts judges every mount by who owns the volume:
//
//   - a volume of the unit's own zone may be mounted freely;
//   - a volume marked shared is the data of a durable unit and is mounted by
//     that unit alone (see sharedVolumeHolder), read-only mounts included;
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
// shared. There are two kinds of shared data, each kept by a durable unit:
// the artifacts unit holds the volume storage.artifacts names, and the state
// unit holds any other.
func (c *checker) sharedVolumeHolder(volume string) manifest.UnitName {
	if store := c.m.Storage.Artifacts; store.Kind == manifest.ArtifactsVolume && store.Location == volume {
		return manifest.UnitArtifacts
	}
	return manifest.UnitState
}

package conformance

import (
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkIsolation verifies the boundaries between the three zones: each has
// its own credentials and its own store role, no unit writes a volume owned
// by another zone (so Zone 1 has no write path into Zone 3), and units that
// face users or run agent code stay on their own network segment.
func (c *checker) checkIsolation() {
	c.checkZoneSecrets()
	c.checkVolumeWrites()
	c.checkSegments()
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
					"%s and %s share credentials %q, so a breach of one zone reaches the other", zoneLabel(earlier), zoneLabel(later), component.CredentialsRef)
			}
			if component.StoreRole != "" && component.StoreRole == other.StoreRole {
				c.add(RuleSharedStoreRole, fmt.Sprintf("components.%s.store_role", later),
					"give each zone a state-store role of its own",
					"%s and %s open the state store with the same role %q, so one zone can read and write the other's data", zoneLabel(earlier), zoneLabel(later), component.StoreRole)
			}
		}
	}
}

// checkVolumeWrites rejects a writable mount of a volume another zone owns.
// A volume marked shared may be written by every zone. The supervisor's
// trust volume has stricter rules of its own and is left to them.
func (c *checker) checkVolumeWrites() {
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
			owner := volume.Zone
			if mount.ReadOnly || mount.Volume == trust || !owner.Valid() || owner.Shared() || owner == name.Zone() {
				continue
			}
			c.add(RuleCrossZoneWrite, path,
				fmt.Sprintf("set read_only: true on this mount, or remove it from units.%s", name),
				"unit %q of %s can write volume %q, which belongs to %s; a zone has no write path into another zone", name, zoneLabel(name.Zone()), mount.Volume, zoneLabel(owner))
		}
	}
}

// checkSegments keeps the application on the app segment and the runners on
// the sandbox segment, from which they reach the outside only through the
// egress proxy.
func (c *checker) checkSegments() {
	only := map[manifest.UnitName]manifest.NetworkName{
		manifest.UnitApp:      manifest.NetworkApp,
		manifest.UnitRunner:   manifest.NetworkSandbox,
		manifest.UnitQARunner: manifest.NetworkSandbox,
	}
	for _, name := range manifest.UnitNames() {
		segment, restricted := only[name]
		if !restricted {
			continue
		}
		for _, attached := range c.m.Units[name].Networks {
			if attached != segment {
				c.add(RuleNetworkSegment, fmt.Sprintf("units.%s.networks", name),
					fmt.Sprintf("attach units.%s to %s only", name, segment),
					"unit %q of %s is attached to the %q segment; it belongs on %q alone, so it has no path into another zone's network", name, zoneLabel(name.Zone()), attached, segment)
			}
		}
	}
}

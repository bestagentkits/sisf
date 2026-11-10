package conformance

import (
	"fmt"
	"slices"

	"github.com/bestagentkits/sisf/internal/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkSupervisor verifies the boundary of the trusted release supervisor.
// The supervisor belongs to the deployment yet stays outside the area a
// candidate can modify only while all four conditions hold:
//
//  1. it runs from its own digest-pinned artifact, not one a candidate is built from;
//  2. the last known good release, the signing keys and the rollback
//     configuration sit on a volume only the supervisor writes and that
//     Zone 1, Zone 2 and agent workspaces cannot reach;
//  3. only a governance cycle approved by a person changes its digest;
//  4. it checks its digest against the signed manifest at startup.
//
// A manifest that declares no supervisor has self-update disabled, which is
// conformant only if it also asks the factory to publish nothing by itself.
func (c *checker) checkSupervisor() {
	if c.m.Supervisor == nil {
		if len(c.m.Release.Units) > 0 {
			c.add(RuleSupervisorUndeclared, "supervisor",
				"declare the supervisor block, or empty release.units so that nothing is published without a person",
				"no trusted release supervisor is declared, so self-update is disabled, yet release.units lets the factory publish %v by itself", c.m.Release.Units)
		}
		return
	}
	c.checkSupervisorArtifact(*c.m.Supervisor)
	c.checkTrustVolume(*c.m.Supervisor)
	c.checkSupervisorGovernance(*c.m.Supervisor)
}

// checkSupervisorArtifact verifies condition 1.
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
	const fix = "build the supervisor as its own artifact, separate from every unit a release can replace"
	for _, name := range manifest.UnitNames() {
		unit, ok := c.m.Units[name]
		if !ok || name == manifest.UnitSupervisor {
			continue
		}
		path := "units." + string(name)
		switch {
		case unit.Digest != "" && unit.Digest == sup.Digest:
			c.add(RuleSupervisorSharesArtifact, path+".digest", fix,
				"unit %q has the supervisor's digest, so the supervisor is built from the same artifact as a candidate", name)
		case unit.Image != "" && unit.Image == sup.Image && unit.Digest == "":
			c.add(RuleSupervisorSharesArtifact, path+".image", fix,
				"unit %q runs the supervisor's image without a digest of its own, so the two cannot be shown to differ", name)
		case declared && len(own.Command) > 0 && len(unit.Command) > 0 && own.Command[0] == unit.Command[0]:
			c.add(RuleSupervisorSharesArtifact, path+".command", fix,
				"unit %q starts the same executable %q as the supervisor, so replacing that unit replaces the supervisor", name, unit.Command[0])
		}
	}
}

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
			path := fmt.Sprintf("units.%s.mounts[%d]", name, i)
			switch {
			case name == manifest.UnitSupervisor:
				held = held || !mount.ReadOnly
			case name.Zone() != core.ZoneControl:
				c.add(RuleTrustVolumeExposed, path,
					fmt.Sprintf("remove the mount of %q from units.%s", sup.TrustVolume, name),
					"unit %q of %s mounts the trust volume; no process outside Zone 3 and no agent workspace may reach the last known good release or the signing keys", name, zoneLabel(name.Zone()))
			case !mount.ReadOnly:
				c.add(RuleTrustVolumeExposed, path,
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

// checkSupervisorGovernance verifies conditions 3 and 4.
func (c *checker) checkSupervisorGovernance(sup manifest.Supervisor) {
	if !sup.DigestChange.HumanApproved() {
		c.add(RuleSupervisorDigestChange, "supervisor.digest_change",
			"set supervisor.digest_change to governance",
			"supervisor.digest_change is %q; only a governance cycle approved by a person may change the supervisor's digest", string(sup.DigestChange))
	}
	if slices.Contains(c.m.Release.Units, manifest.UnitSupervisor) {
		c.add(RuleSupervisorDigestChange, "release.units",
			"remove supervisor from release.units",
			"release.units lets the normal release pipeline publish the supervisor, which puts it inside a candidate's reach")
	}
	if !sup.StartupSelfCheck {
		c.add(RuleSupervisorSelfCheck, "supervisor.startup_self_check",
			"set supervisor.startup_self_check to true",
			"the supervisor does not check its own digest at startup, so a replaced supervisor would run unnoticed")
	}
	if sup.RootKeyRef == "" {
		c.add(RuleSupervisorSelfCheck, "supervisor.root_key_ref",
			"set supervisor.root_key_ref to where the root public key is found, for example env:SISF_ROOT_PUBLIC_KEY",
			"no root public key is referenced, so the supervisor has nothing to verify the signed manifest with")
	}
}

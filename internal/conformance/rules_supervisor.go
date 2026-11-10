package conformance

import (
	"path"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkSupervisor verifies the boundary of the trusted release supervisor.
// The supervisor belongs to the deployment yet stays outside the area a
// candidate can modify only while all four conditions hold:
//
//  1. it runs from its own digest-pinned artifact, not one a candidate is built from;
//  2. the last known good release, the signing keys and the rollback
//     configuration sit on a volume only the supervisor writes, that Zone 1,
//     Zone 2, agent workspaces and every unit a release can replace cannot
//     reach, and that holds nothing else;
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
	c.checkTrustVolumeStorage(*c.m.Supervisor)
	c.checkSupervisorGovernance(*c.m.Supervisor)
}

// imageRepository returns an image reference without its tag and digest, so
// that two builds of one repository are recognized as the same artifact line.
func imageRepository(image string) string {
	image, _, _ = strings.Cut(image, "@")
	if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
		image = image[:colon]
	}
	return image
}

// checkSupervisorArtifact verifies condition 1. Another unit shares the
// supervisor's artifact when it has the supervisor's digest, when its image
// is the supervisor's repository under any tag or digest, or when it starts
// an executable of the same name from any directory.
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
	repository, executable := imageRepository(sup.Image), ""
	if declared && len(own.Command) > 0 {
		executable = path.Base(own.Command[0])
	}
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
		case unit.Image != "" && repository != "" && strings.EqualFold(imageRepository(unit.Image), repository):
			c.add(RuleSupervisorSharesArtifact, at+".image", fix,
				"unit %q runs image %q, which is the supervisor's repository %q whatever its tag or digest, so a candidate built there is built where the supervisor is", name, unit.Image, repository)
		case executable != "" && len(unit.Command) > 0 && path.Base(unit.Command[0]) == executable:
			c.add(RuleSupervisorSharesArtifact, at+".command", fix,
				"unit %q starts executable %q, the one the supervisor starts, so replacing that unit replaces the supervisor", name, unit.Command[0])
		}
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
	// The value is not repeated: what stands here in place of a reference
	// may be key material.
	if !manifest.IsSecretRef(sup.RootKeyRef) {
		c.add(RuleSupervisorSelfCheck, "supervisor.root_key_ref",
			"set supervisor.root_key_ref to where the root public key is found, for example env:SISF_ROOT_PUBLIC_KEY",
			"supervisor.root_key_ref does not hold %s, so no root public key is referenced and the supervisor has nothing to verify the signed manifest with", manifest.SecretRefForm)
	}
}

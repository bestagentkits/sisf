package conformance

import (
	"fmt"
	"slices"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkStorage keeps the two durable stores where their own unit holds
// them. Every zone uses the state store and the artifact store through the
// state and artifacts units; a store kept in a zone's files, or in files no
// unit holds, would be that zone's to rewrite or nobody's to guard.
//
// A store placed on the supervisor's trust volume is refused by the rule of
// that volume, which says the more serious thing, and is not reported twice.
func (c *checker) checkStorage() {
	c.checkStatePlacement()
	c.checkArtifactsPlacement()
}

// checkStatePlacement requires the file of the state store to lie in a
// directory at which the state unit mounts a shared volume it holds.
func (c *checker) checkStatePlacement() {
	const at = "storage.state.path"
	file := c.m.Storage.State.Path
	state, declared := c.m.Units[manifest.UnitState]
	if file == "" || !declared || c.reported(RuleTrustVolumeStorage, at) {
		return
	}
	held := slices.ContainsFunc(state.Mounts, func(mount manifest.Mount) bool {
		volume, known := c.m.Volumes[mount.Volume]
		return known && volume.Zone.Shared() && c.sharedVolumeHolder(mount.Volume) == manifest.UnitState &&
			mount.Path != "" && within(file, mount.Path)
	})
	if !held {
		c.add(RuleStatePlacement, at,
			"set storage.state.path to a file below the directory at which units.state mounts a volume marked zone: shared",
			"storage.state.path places the state store outside every shared volume the state unit holds; the records of all three zones would lie in files that a zone owns or that no unit guards")
	}
}

// checkArtifactsPlacement requires artifacts kept on a volume to be on a
// declared volume marked shared that the artifacts unit holds and mounts.
// The location is not repeated in a finding.
func (c *checker) checkArtifactsPlacement() {
	const at = "storage.artifacts.location"
	store := c.m.Storage.Artifacts
	if store.Kind != manifest.ArtifactsVolume || store.Location == "" || c.reported(RuleTrustVolumeStorage, at) {
		return
	}
	const fix = "set storage.artifacts.location to a volume declared with zone: shared that units.artifacts mounts, or set storage.artifacts.kind to object_store"
	volume, declared := c.m.Volumes[store.Location]
	artifacts, unitDeclared := c.m.Units[manifest.UnitArtifacts]
	switch {
	case !declared:
		c.add(RuleArtifactsPlacement, at, fix,
			"storage.artifacts.location names no declared volume, so nothing says who owns the place where artifacts, logs and snapshots are kept")
	case !volume.Zone.Shared():
		c.add(RuleArtifactsPlacement, at, fix,
			"storage.artifacts.location names a volume of %s; the evidence, logs and snapshots of every zone would be that zone's to rewrite", zoneLabel(volume.Zone))
	case c.sharedVolumeHolder(store.Location) != manifest.UnitArtifacts:
		c.add(RuleArtifactsPlacement, at,
			fmt.Sprintf("remove holder from volumes.%s or set it to artifacts, or name another volume in storage.artifacts.location", store.Location),
			"storage.artifacts.location names a shared volume held by the %s unit; artifacts are kept by the artifacts unit alone", c.sharedVolumeHolder(store.Location))
	case unitDeclared && !slices.ContainsFunc(artifacts.Mounts, func(mount manifest.Mount) bool { return mount.Volume == store.Location }):
		c.add(RuleArtifactsPlacement, at,
			fmt.Sprintf("mount volume %q in units.artifacts", store.Location),
			"the artifacts unit does not mount the volume storage.artifacts.location names, so the artifact store is kept somewhere no unit holds")
	}
}

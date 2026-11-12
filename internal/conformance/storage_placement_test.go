package conformance

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func TestTheStateStoreFileLiesOnTheVolumeTheStateUnitHolds(t *testing.T) {
	t.Parallel()
	misplaced := []string{
		"data/workspaces/factory.db", // in an agent workspace
		"data/qa-workspaces/factory.db",
		"data/app/factory.db",     // in the application's data
		"data/control/factory.db", // in Zone 3's data
		"data/artifacts/factory.db",
		"data/state-backup/factory.db", // beside the state volume, not on it
		"data/factory.db",
		"factory.db",
		"/var/lib/sisf/factory.db", // nowhere the manifest declares
	}
	for _, file := range misplaced {
		t.Run(file, func(t *testing.T) {
			t.Parallel()
			m := standalone(t)
			m.Storage.State.Path = file
			report := Check(m)
			wantOnly(t, report, RuleStatePlacement, "storage.state.path")
			if len(report.Findings) != 1 {
				t.Fatalf("one misplaced store is one finding, got %+v", report.Findings)
			}
		})
	}
	for _, file := range []string{"data/state/factory.db", "data/state/db/factory.db", "./data/app/../state/factory.db"} {
		m := standalone(t)
		m.Storage.State.Path = file
		if report := Check(m); !report.Conformant() {
			t.Errorf("%s lies on the state volume, got %+v", file, report.Findings)
		}
	}

	// The volume must be one the state unit holds, not merely one it mounts.
	m := standalone(t)
	m.Volumes["state-data"] = manifest.Volume{Zone: core.ZoneShared, Holder: manifest.UnitEdge}
	if report := Check(m); !report.Has(RuleStatePlacement) {
		t.Fatalf("a volume another unit holds is not the state unit's, got %+v", report.Findings)
	}
	m = standalone(t)
	editUnit(m, manifest.UnitState, func(u *manifest.Unit) { u.Mounts = nil })
	wantOnly(t, Check(m), RuleStatePlacement, "storage.state.path")
}

func TestArtifactsKeptOnAVolumeAreOnASharedVolumeTheArtifactsUnitHolds(t *testing.T) {
	t.Parallel()
	for mode, valid := range bothModes {
		for _, location := range []string{"workspaces", "qa-workspaces", "app-data", "control-data", "evidence", "/var/lib/sisf/artifacts"} {
			t.Run(location+"/"+mode, func(t *testing.T) {
				t.Parallel()
				m := valid(t)
				m.Storage.Artifacts.Location = location
				report := Check(m)
				wantOnly(t, report, RuleArtifactsPlacement, "storage.artifacts.location")
				if len(report.Findings) != 1 {
					t.Fatalf("one misplaced store is one finding, got %+v", report.Findings)
				}
			})
		}
		// The artifacts unit must hold the volume: mount it, and be its holder.
		m := valid(t)
		editUnit(m, manifest.UnitArtifacts, func(u *manifest.Unit) { u.Mounts = nil })
		wantOnly(t, Check(m), RuleArtifactsPlacement, "storage.artifacts.location")

		m = valid(t)
		m.Volumes["artifacts"] = manifest.Volume{Zone: core.ZoneShared, Holder: manifest.UnitState}
		if report := Check(m); !report.reportedAt(RuleArtifactsPlacement, "storage.artifacts.location") {
			t.Fatalf("%s: a volume the state unit holds is not the artifacts unit's, got %+v", mode, report.Findings)
		}

		// The state unit's own volume is not where artifacts go either: the
		// artifacts unit does not mount it, and the state unit loses its claim.
		m = valid(t)
		m.Storage.Artifacts.Location = "state-data"
		if report := Check(m); !report.reportedAt(RuleArtifactsPlacement, "storage.artifacts.location") || !report.Has(RuleSharedVolumeMount) {
			t.Fatalf("%s: artifacts on the state volume must be refused, got %+v", mode, report.Findings)
		}

		// A holder left out defaults to the artifacts unit for this volume.
		m = valid(t)
		m.Volumes["artifacts"] = manifest.Volume{Zone: core.ZoneShared}
		if report := Check(m); !report.Conformant() {
			t.Fatalf("%s: the artifact volume is held by the artifacts unit by default, got %+v", mode, report.Findings)
		}
	}

	// An object store is not a volume of the deployment and is placed by its own address.
	m := docker(t)
	m.Storage.Artifacts = manifest.ArtifactStore{Kind: manifest.ArtifactsObjectStore, Location: "s3://acme-sisf-artifacts"}
	if report := Check(m); !report.Conformant() {
		t.Fatalf("an object store needs no volume, got %+v", report.Findings)
	}
}

// The edge keeps TLS state. A shared volume that names the edge as its
// holder is mounted by the edge alone, as the state volume is by the state
// unit.
func TestASharedVolumeMayNameTheUnitThatHoldsIt(t *testing.T) {
	t.Parallel()
	withEdgeVolume := func(t *testing.T, valid func(*testing.T) *manifest.Manifest, dir string) *manifest.Manifest {
		m := valid(t)
		m.Volumes["edge-tls"] = manifest.Volume{Zone: core.ZoneShared, Holder: manifest.UnitEdge}
		editUnit(m, manifest.UnitEdge, func(u *manifest.Unit) {
			u.Mounts = append(u.Mounts, manifest.Mount{Volume: "edge-tls", Path: dir})
		})
		return m
	}
	if report := Check(withEdgeVolume(t, docker, "/var/lib/edge")); !report.Conformant() {
		t.Fatalf("the edge may hold a writable volume of its own, got %+v", report.Findings)
	}
	if report := Check(withEdgeVolume(t, standalone, "data/edge")); !report.Conformant() {
		t.Fatalf("the edge may hold a writable volume of its own on one machine, got %+v", report.Findings)
	}
	// Nobody else mounts it, the state unit included.
	for _, unit := range []manifest.UnitName{manifest.UnitApp, manifest.UnitRunner, manifest.UnitControl, manifest.UnitState} {
		m := withEdgeVolume(t, docker, "/var/lib/edge")
		mount(m, unit, "edge-tls", true)
		wantOnly(t, Check(m), RuleSharedVolumeMount, "units."+string(unit)+".mounts[1]")
	}
	// Without a named holder the edge has no claim to a shared volume.
	m := withEdgeVolume(t, docker, "/var/lib/edge")
	m.Volumes["edge-tls"] = manifest.Volume{Zone: core.ZoneShared}
	wantOnly(t, Check(m), RuleSharedVolumeMount, "units.edge.mounts[0]")

	refused := map[string]manifest.Volume{
		"a unit of Zone 1 as holder":       {Zone: core.ZoneShared, Holder: manifest.UnitApp},
		"a runner as holder":               {Zone: core.ZoneShared, Holder: manifest.UnitRunner},
		"the supervisor as holder":         {Zone: core.ZoneShared, Holder: manifest.UnitSupervisor},
		"a unit that does not exist":       {Zone: core.ZoneShared, Holder: "worker"},
		"a holder on a volume a zone owns": {Zone: core.ZoneControl, Holder: manifest.UnitEdge},
	}
	for name, volume := range refused {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := docker(t)
			m.Volumes["cache"] = volume
			report := Check(m)
			wantOnly(t, report, RuleVolumeHolder, "volumes.cache.holder")
			if len(report.Findings) != 1 {
				t.Fatalf("one wrong holder is one finding, reported once, got %+v", report.Findings)
			}
		})
	}
	// A holder that is refused gives its unit no claim to the volume.
	m = docker(t)
	m.Volumes["cache"] = manifest.Volume{Zone: core.ZoneShared, Holder: manifest.UnitRunner}
	mount(m, manifest.UnitRunner, "cache", false)
	if report := Check(m); !report.Has(RuleVolumeHolder) || !report.Has(RuleSharedVolumeMount) {
		t.Fatalf("a runner named as holder still may not mount shared data, got %+v", report.Findings)
	}
}

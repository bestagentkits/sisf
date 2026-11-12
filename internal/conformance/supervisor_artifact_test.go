package conformance

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// A unit without a digest is refused for that alone; it must still be seen
// to share the supervisor's artifact, so that self-update stays disabled.
func TestAUnitRunningTheSupervisorImageWithoutADigestSharesItsArtifact(t *testing.T) {
	t.Parallel()
	m := docker(t)
	editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Image, u.Digest = m.Supervisor.Image, "" })
	report := Check(m)
	if !report.Has(RuleSupervisorSharesArtifact) || !report.Has(RuleUnitNotPinned) || report.SelfUpdate {
		t.Fatalf("want the shared artifact and the missing digest both reported and self-update disabled, got %+v", report)
	}
}

func TestAnImageIsComparedByRepositoryWhateverItsTagOrDigest(t *testing.T) {
	t.Parallel()
	const digest = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
	cases := map[string]string{
		"ghcr.io/acme/sisf-supervisor":              "ghcr.io/acme/sisf-supervisor",
		"ghcr.io/acme/sisf-supervisor:v2":           "ghcr.io/acme/sisf-supervisor",
		"ghcr.io/acme/sisf-supervisor@" + digest:    "ghcr.io/acme/sisf-supervisor",
		"ghcr.io/acme/sisf-supervisor:v2@" + digest: "ghcr.io/acme/sisf-supervisor",
		"localhost:5000/sisf-supervisor":            "localhost:5000/sisf-supervisor", // a registry port is not a tag
		"localhost:5000/sisf-supervisor:v2":         "localhost:5000/sisf-supervisor",
		"sisf-supervisor:latest":                    "sisf-supervisor",
		"bin/sisf-supervisor":                       "bin/sisf-supervisor", // a binary of a standalone install
		"":                                          "",
	}
	for image, want := range cases {
		if got := imageRepository(image); got != want {
			t.Errorf("imageRepository(%q) = %q, want %q", image, got, want)
		}
	}
}

// Storage beside the trust volume, or merely named like it, is not on it.
func TestStorageOutsideTheTrustVolumeIsAllowed(t *testing.T) {
	t.Parallel()
	// A shared volume the artifacts unit holds, whatever it is called.
	for _, volume := range []string{"artifacts", "trusted-artifacts", "trust-archive"} {
		m := docker(t)
		delete(m.Volumes, "artifacts")
		m.Volumes[volume] = manifest.Volume{Zone: core.ZoneShared}
		m.Storage.Artifacts.Location = volume
		editUnit(m, manifest.UnitArtifacts, func(u *manifest.Unit) {
			u.Mounts = []manifest.Mount{{Volume: volume, Path: "/var/lib/sisf/trust-archive"}}
		})
		if report := Check(m); !report.Conformant() {
			t.Errorf("artifact volume %q is outside the trust volume, got %+v", volume, report.Findings)
		}
	}
	// An object store addressed beside the trust volume.
	for _, location := range []string{"s3://bucket/trust", "/var/lib/sisf/trust-archive"} {
		m := docker(t)
		m.Storage.Artifacts = manifest.ArtifactStore{Kind: manifest.ArtifactsObjectStore, Location: location}
		if report := Check(m); !report.Conformant() {
			t.Errorf("object store %q is outside the trust volume, got %+v", location, report.Findings)
		}
	}
}

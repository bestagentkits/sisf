package conformance

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func TestSelfUpdateIsEnabledOnlyWhileTheSupervisorBoundaryHolds(t *testing.T) {
	for _, m := range []*manifest.Manifest{docker(t), standalone(t)} {
		if report := Check(m); !report.Conformant() || !report.SelfUpdate {
			t.Fatalf("a supervisor meeting all four conditions enables self-update, got %+v", report)
		}
	}
}

// Each case breaks exactly one of the four conditions that keep the
// supervisor outside the area a candidate can modify.
func TestABreachOfAnySupervisorBoundaryConditionIsRejected(t *testing.T) {
	const otherDigest = core.Digest("sha256:9999999999999999999999999999999999999999999999999999999999999999")
	cases := []struct {
		name      string
		condition int
		valid     func(*testing.T) *manifest.Manifest
		breach    func(m *manifest.Manifest)
		rule      Rule
		path      string
	}{
		{"the supervisor is named by a tag", 1, docker,
			func(m *manifest.Manifest) {
				m.Supervisor.Digest = "latest"
				editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Digest = "" })
			}, RuleSupervisorNotPinned, "supervisor.digest"},
		{"the supervisor has no digest", 1, docker,
			func(m *manifest.Manifest) {
				m.Supervisor.Digest = ""
				editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Digest = "" })
			}, RuleSupervisorNotPinned, "supervisor.digest"},
		{"the supervisor names no artifact", 1, docker,
			func(m *manifest.Manifest) {
				m.Supervisor.Image = ""
				editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Image = "" })
			}, RuleSupervisorNotPinned, "supervisor.image"},
		{"the supervisor unit runs another digest than the pinned one", 1, docker,
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Digest = otherDigest })
			}, RuleSupervisorNotPinned, "units.supervisor.digest"},
		{"the supervisor unit runs another image than the pinned one", 1, docker,
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Image = "ghcr.io/acme/sisf-factoryd" })
			}, RuleSupervisorNotPinned, "units.supervisor.image"},
		{"the supervisor has the digest of the control unit", 1, docker,
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitControl, func(u *manifest.Unit) { u.Digest = m.Supervisor.Digest })
			}, RuleSupervisorSharesArtifact, "units.control.digest"},
		{"the app runs the supervisor image under a digest of its own", 1, docker,
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Image = m.Supervisor.Image })
			}, RuleSupervisorSharesArtifact, "units.app.image"},
		{"the control unit runs the supervisor image under another tag", 1, docker,
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitControl, func(u *manifest.Unit) { u.Image = m.Supervisor.Image + ":v2" })
			}, RuleSupervisorSharesArtifact, "units.control.image"},
		{"the dispatcher runs the supervisor image named by another digest", 1, docker,
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitDispatcher, func(u *manifest.Unit) { u.Image = m.Supervisor.Image + "@" + string(otherDigest) })
			}, RuleSupervisorSharesArtifact, "units.dispatcher.image"},
		{"the edge runs the supervisor image under a tag and a digest", 1, docker,
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitEdge, func(u *manifest.Unit) { u.Image = m.Supervisor.Image + ":stable@" + string(otherDigest) })
			}, RuleSupervisorSharesArtifact, "units.edge.image"},
		{"the supervisor starts the executable of the dispatcher", 1, standalone,
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitDispatcher, func(u *manifest.Unit) { u.Command = []string{"bin/sisf-supervisor", "dispatch"} })
			}, RuleSupervisorSharesArtifact, "units.dispatcher.command"},
		{"the supervisor starts the executable of the dispatcher from another directory", 1, standalone,
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitDispatcher, func(u *manifest.Unit) { u.Command = []string{"/usr/local/bin/sisf-supervisor", "dispatch"} })
			}, RuleSupervisorSharesArtifact, "units.dispatcher.command"},
		// Where no image stands for a unit, its executable does, from any directory.
		{"the supervisor is the control unit's executable under a full path", 1, standalone,
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitControl, func(u *manifest.Unit) { u.Command = []string{"factoryd", "control"} })
				m.Supervisor.Image = "/usr/bin/factoryd"
				editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) {
					u.Image, u.Command = "/usr/bin/factoryd", []string{"/usr/bin/factoryd", "supervisor"}
				})
			}, RuleSupervisorSharesArtifact, "units.control.command"},

		{"the trust volume is not named", 2, docker,
			func(m *manifest.Manifest) { m.Supervisor.TrustVolume = "" }, RuleTrustVolumeUndeclared, "supervisor.trust_volume"},
		{"the trust volume is not declared", 2, docker,
			func(m *manifest.Manifest) { m.Supervisor.TrustVolume = "keys" }, RuleTrustVolumeUndeclared, "supervisor.trust_volume"},
		{"every zone may write the trust volume", 2, docker,
			func(m *manifest.Manifest) { m.Volumes["trust"] = manifest.Volume{Zone: core.ZoneShared} }, RuleTrustVolumeExposed, "volumes.trust.zone"},
		{"Zone 1 owns the trust volume", 2, docker,
			func(m *manifest.Manifest) { m.Volumes["trust"] = manifest.Volume{Zone: core.ZoneApplication} }, RuleTrustVolumeExposed, "volumes.trust.zone"},
		{"Zone 2 owns the trust volume", 2, docker,
			func(m *manifest.Manifest) { m.Volumes["trust"] = manifest.Volume{Zone: core.ZoneAgent} }, RuleTrustVolumeExposed, "volumes.trust.zone"},
		{"the app can write the trust volume", 2, docker,
			func(m *manifest.Manifest) { mount(m, manifest.UnitApp, "trust", false) }, RuleTrustVolumeExposed, "units.app.mounts[1]"},
		{"the app can read the signing keys", 2, docker,
			func(m *manifest.Manifest) { mount(m, manifest.UnitApp, "trust", true) }, RuleTrustVolumeExposed, "units.app.mounts[1]"},
		{"the dispatcher can write the trust volume", 2, docker,
			func(m *manifest.Manifest) { mount(m, manifest.UnitDispatcher, "trust", false) }, RuleTrustVolumeExposed, "units.dispatcher.mounts[0]"},
		{"an agent workspace holds the trust volume", 2, docker,
			func(m *manifest.Manifest) { mount(m, manifest.UnitRunner, "trust", false) }, RuleTrustVolumeExposed, "units.runner.mounts[1]"},
		{"the QA workspace can read the trust volume", 2, docker,
			func(m *manifest.Manifest) { mount(m, manifest.UnitQARunner, "trust", true) }, RuleTrustVolumeExposed, "units.qa-runner.mounts[1]"},
		{"a shared unit holds the trust volume", 2, docker,
			func(m *manifest.Manifest) { mount(m, manifest.UnitEdge, "trust", true) }, RuleTrustVolumeExposed, "units.edge.mounts[0]"},
		{"the control unit, which a release can replace, can write the trust volume", 2, docker,
			func(m *manifest.Manifest) { mount(m, manifest.UnitControl, "trust", false) }, RuleTrustVolumeExposed, "units.control.mounts[1]"},
		{"a unit the factory publishes by itself can read the signing keys", 2, docker,
			func(m *manifest.Manifest) {
				m.Release.Units = append(m.Release.Units, manifest.UnitControl)
				mount(m, manifest.UnitControl, "trust", true)
			}, RuleTrustVolumeExposed, "units.control.mounts[1]"},
		{"the artifact store is kept on the trust volume", 2, docker,
			func(m *manifest.Manifest) { m.Storage.Artifacts.Location = "trust" }, RuleTrustVolumeStorage, "storage.artifacts.location"},
		{"the artifact store is kept in a directory of the trust volume", 2, docker,
			func(m *manifest.Manifest) { m.Storage.Artifacts.Location = "trust/artifacts" }, RuleTrustVolumeStorage, "storage.artifacts.location"},
		{"the artifact store is kept where the supervisor mounts the trust volume", 2, docker,
			func(m *manifest.Manifest) { m.Storage.Artifacts.Location = "/var/lib/sisf/trust/../trust/artifacts" }, RuleTrustVolumeStorage, "storage.artifacts.location"},
		{"an object store is addressed inside the trust volume", 2, docker,
			func(m *manifest.Manifest) {
				m.Storage.Artifacts = manifest.ArtifactStore{Kind: manifest.ArtifactsObjectStore, Location: "/var/lib/sisf/trust"}
			}, RuleTrustVolumeStorage, "storage.artifacts.location"},
		{"the state store file is kept on the trust volume", 2, standalone,
			func(m *manifest.Manifest) { m.Storage.State.Path = "data/trust/factory.db" }, RuleTrustVolumeStorage, "storage.state.path"},
		{"the supervisor does not mount the trust volume", 2, docker,
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Mounts = nil })
			}, RuleTrustVolumeUnmounted, "units.supervisor.mounts"},
		{"the supervisor cannot write the trust volume", 2, docker,
			func(m *manifest.Manifest) { m.Units[manifest.UnitSupervisor].Mounts[0].ReadOnly = true }, RuleTrustVolumeUnmounted, "units.supervisor.mounts"},

		{"the release pipeline may change the supervisor digest", 3, docker,
			func(m *manifest.Manifest) { m.Supervisor.DigestChange = manifest.DigestChangePipeline }, RuleSupervisorDigestChange, "supervisor.digest_change"},
		{"nobody is named to change the supervisor digest", 3, docker,
			func(m *manifest.Manifest) { m.Supervisor.DigestChange = "" }, RuleSupervisorDigestChange, "supervisor.digest_change"},
		{"the factory publishes the supervisor by itself", 3, docker,
			func(m *manifest.Manifest) { m.Release.Units = append(m.Release.Units, manifest.UnitSupervisor) }, RuleSupervisorDigestChange, "release.units"},

		{"the supervisor does not check its digest at startup", 4, docker,
			func(m *manifest.Manifest) { m.Supervisor.StartupSelfCheck = false }, RuleSupervisorSelfCheck, "supervisor.startup_self_check"},
		{"there is no root key to check the signed manifest with", 4, docker,
			func(m *manifest.Manifest) { m.Supervisor.RootKeyRef = "" }, RuleSupervisorSelfCheck, "supervisor.root_key_ref"},
	}
	covered := map[int]bool{}
	for _, tc := range cases {
		covered[tc.condition] = true
		t.Run(tc.name, func(t *testing.T) {
			m := tc.valid(t)
			tc.breach(m)
			report := Check(m)
			wantOnly(t, report, tc.rule, tc.path)
			if report.SelfUpdate {
				t.Errorf("condition %d is broken, so self-update must be disabled", tc.condition)
			}
		})
	}
	if len(covered) != 4 {
		t.Fatalf("every one of the four conditions needs a breach, covered %v", covered)
	}
}

func TestTheControlUnitMayReadButNotWriteTheTrustVolume(t *testing.T) {
	m := docker(t)
	mount(m, manifest.UnitControl, "trust", true)
	if report := Check(m); !report.Conformant() {
		t.Fatalf("a read-only mount by a Zone 3 unit keeps the boundary, got %+v", report.Findings)
	}
}

func TestWithoutASupervisorNothingIsPublishedWithoutAPerson(t *testing.T) {
	m := docker(t)
	m.Supervisor = nil
	report := Check(m)
	wantOnly(t, report, RuleSupervisorUndeclared, "supervisor")
	if report.SelfUpdate {
		t.Error("self-update must be disabled when no supervisor is declared")
	}

	m.Release.Units = []manifest.UnitName{}
	report = Check(m)
	if !report.Conformant() || report.SelfUpdate {
		t.Fatalf("no supervisor and nothing self-published is conformant with self-update disabled, got %+v", report)
	}
}

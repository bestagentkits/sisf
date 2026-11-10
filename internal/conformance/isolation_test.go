package conformance

import (
	"fmt"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func TestNoTwoZonesShareCredentialsOrAStoreRole(t *testing.T) {
	zones := core.Zones()
	for i, later := range zones {
		for _, earlier := range zones[:i] {
			m := docker(t)
			m.Components.Zone(later).CredentialsRef = m.Components.Zone(earlier).CredentialsRef
			wantOnly(t, Check(m), RuleSharedCredentials, fmt.Sprintf("components.%s.credentials_ref", later))

			m = docker(t)
			m.Components.Zone(later).StoreRole = m.Components.Zone(earlier).StoreRole
			wantOnly(t, Check(m), RuleSharedStoreRole, fmt.Sprintf("components.%s.store_role", later))
		}
	}
	// One credential set for the whole deployment breaks every boundary at once.
	m := docker(t)
	for _, zone := range zones {
		m.Components.Zone(zone).CredentialsRef = "local:factory"
	}
	report := Check(m)
	if len(report.Findings) != 3 {
		t.Fatalf("three zones on one credential set are three shared pairs, got %+v", report.Findings)
	}
	wantOnly(t, report, RuleSharedCredentials, "components.control.credentials_ref")
}

func TestNoUnitWritesAVolumeOwnedByAnotherZone(t *testing.T) {
	forbidden := []struct {
		unit   manifest.UnitName
		volume string
	}{
		{manifest.UnitApp, "control-data"},        // Zone 1 into Zone 3
		{manifest.UnitApp, "workspaces"},          // Zone 1 into Zone 2
		{manifest.UnitDispatcher, "control-data"}, // Zone 2 into Zone 3
		{manifest.UnitRunner, "app-data"},         // Zone 2 into Zone 1
		{manifest.UnitQARunner, "control-data"},   // QA must not rewrite its own baseline
		{manifest.UnitControl, "app-data"},        // Zone 3 into Zone 1
		{manifest.UnitEdge, "control-data"},       // a unit every zone uses into Zone 3
	}
	for _, tc := range forbidden {
		t.Run(fmt.Sprintf("%s writes %s", tc.unit, tc.volume), func(t *testing.T) {
			m := docker(t)
			mount(m, tc.unit, tc.volume, false)
			path := fmt.Sprintf("units.%s.mounts[%d]", tc.unit, len(m.Units[tc.unit].Mounts)-1)
			wantOnly(t, Check(m), RuleCrossZoneWrite, path)
		})
	}
}

// The application and the agents reach another zone through its API. A
// read-only mount is still a path around it: Zone 1 would read Zone 3's
// files, and a runner would read what the control plane keeps.
func TestZone1AndZone2MountOnlyVolumesOfTheirOwnZone(t *testing.T) {
	t.Parallel()
	forbidden := []struct {
		unit   manifest.UnitName
		volume string
	}{
		{manifest.UnitApp, "control-data"},        // Zone 1 reading Zone 3
		{manifest.UnitApp, "workspaces"},          // Zone 1 reading Zone 2
		{manifest.UnitRunner, "control-data"},     // an agent workspace reading Zone 3
		{manifest.UnitRunner, "app-data"},         // an agent workspace reading customer data
		{manifest.UnitQARunner, "control-data"},   // QA gets its baseline in its workspace, not from Zone 3's files
		{manifest.UnitQARunner, "app-data"},       //
		{manifest.UnitDispatcher, "control-data"}, // Zone 2 calls Zone 3 only through the API
		{manifest.UnitDispatcher, "app-data"},     //
	}
	for _, tc := range forbidden {
		t.Run(fmt.Sprintf("%s reads %s", tc.unit, tc.volume), func(t *testing.T) {
			m := docker(t)
			mount(m, tc.unit, tc.volume, true)
			path := fmt.Sprintf("units.%s.mounts[%d]", tc.unit, len(m.Units[tc.unit].Mounts)-1)
			wantOnly(t, Check(m), RuleCrossZoneRead, path)
		})
	}
}

// A volume marked shared is the data of the state unit or of the artifacts
// unit. Every zone uses it through that unit; none mounts it.
func TestASharedVolumeIsMountedOnlyByTheUnitThatHoldsIt(t *testing.T) {
	t.Parallel()
	for _, unit := range manifest.UnitNames() {
		if unit == manifest.UnitState {
			continue
		}
		for _, readOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s mounts the state volume, read only %v", unit, readOnly), func(t *testing.T) {
				m := docker(t)
				mount(m, unit, "state-data", readOnly)
				path := fmt.Sprintf("units.%s.mounts[%d]", unit, len(m.Units[unit].Mounts)-1)
				wantOnly(t, Check(m), RuleSharedVolumeMount, path)
			})
		}
	}

	// The volume the artifact store is kept on is held by the artifacts unit.
	withArtifactVolume := func() *manifest.Manifest {
		m := docker(t)
		m.Volumes["artifacts"] = manifest.Volume{Zone: core.ZoneShared}
		if m.Storage.Artifacts.Kind != manifest.ArtifactsVolume || m.Storage.Artifacts.Location != "artifacts" {
			t.Fatalf("the example must keep its artifacts on the volume named artifacts, got %+v", m.Storage.Artifacts)
		}
		return m
	}
	m := withArtifactVolume()
	mount(m, manifest.UnitArtifacts, "artifacts", false)
	if report := Check(m); !report.Conformant() {
		t.Fatalf("the artifacts unit holds the artifact volume, got %+v", report.Findings)
	}
	for _, unit := range []manifest.UnitName{manifest.UnitState, manifest.UnitRunner, manifest.UnitQARunner, manifest.UnitApp, manifest.UnitEdge} {
		m := withArtifactVolume()
		mount(m, unit, "artifacts", unit != manifest.UnitRunner)
		path := fmt.Sprintf("units.%s.mounts[%d]", unit, len(m.Units[unit].Mounts)-1)
		wantOnly(t, Check(m), RuleSharedVolumeMount, path)
	}
}

func TestMountsThatKeepTheZonesApartAreAllowed(t *testing.T) {
	t.Parallel()
	allowed := []struct {
		unit     manifest.UnitName
		volume   string
		readOnly bool
	}{
		{manifest.UnitControl, "app-data", true},       // Zone 3 reading application logs
		{manifest.UnitSupervisor, "app-data", true},    //
		{manifest.UnitEdge, "app-data", true},          // the edge serving the application's files
		{manifest.UnitDispatcher, "workspaces", false}, // the unit's own zone
		{manifest.UnitSupervisor, "control-data", false},
	}
	for _, tc := range allowed {
		m := docker(t)
		mount(m, tc.unit, tc.volume, tc.readOnly)
		if report := Check(m); !report.Conformant() {
			t.Errorf("%s mounting %s (read only: %v) must be allowed, got %+v", tc.unit, tc.volume, tc.readOnly, report.Findings)
		}
	}
	// The state unit is the one holder of the state volume in both modes.
	for _, m := range []*manifest.Manifest{docker(t), standalone(t)} {
		if held := m.Units[manifest.UnitState].Mounts; len(held) != 1 || held[0].Volume != "state-data" || held[0].ReadOnly {
			t.Errorf("the %s example must have the state unit hold the state volume, got %+v", m.Mode, held)
		}
	}
}

func TestAMountedVolumeMustHaveAnOwner(t *testing.T) {
	m := docker(t)
	mount(m, manifest.UnitApp, "uploads", true)
	wantOnly(t, Check(m), RuleVolumeUndeclared, "units.app.mounts[1].volume")
}

func TestTheApplicationAndTheRunnersStayOnTheirOwnSegment(t *testing.T) {
	cases := []struct {
		unit    manifest.UnitName
		segment manifest.NetworkName
	}{
		{manifest.UnitApp, manifest.NetworkControl}, // a path from Zone 1 into Zone 3
		{manifest.UnitApp, manifest.NetworkFactory},
		{manifest.UnitApp, manifest.NetworkSandbox},
		{manifest.UnitRunner, manifest.NetworkFactory}, // around the egress proxy
		{manifest.UnitRunner, manifest.NetworkControl},
		{manifest.UnitQARunner, manifest.NetworkApp},
	}
	for _, tc := range cases {
		for _, valid := range []func(*testing.T) *manifest.Manifest{docker, standalone} {
			m := valid(t)
			editUnit(m, tc.unit, func(u *manifest.Unit) { u.Networks = append(u.Networks, tc.segment) })
			wantOnly(t, Check(m), RuleNetworkSegment, fmt.Sprintf("units.%s.networks", tc.unit))
		}
	}
}

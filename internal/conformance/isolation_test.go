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
		m.Components.Zone(zone).CredentialsRef = "factory"
	}
	if report := Check(m); len(report.Findings) != 3 {
		t.Fatalf("three zones on one credential set are three shared pairs, got %+v", report.Findings)
	}
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

func TestReadingAcrossZonesAndWritingSharedVolumesIsAllowed(t *testing.T) {
	allowed := []struct {
		unit     manifest.UnitName
		volume   string
		readOnly bool
	}{
		{manifest.UnitQARunner, "control-data", true},  // a read-only test baseline
		{manifest.UnitControl, "app-data", true},       // reading application logs
		{manifest.UnitApp, "state-data", false},        // a volume marked shared
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

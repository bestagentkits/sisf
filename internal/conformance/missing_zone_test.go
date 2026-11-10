package conformance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// dropZone removes the component of one zone.
func dropZone(m *manifest.Manifest, zone core.Zone) {
	switch zone {
	case core.ZoneApplication:
		m.Components.Application = nil
	case core.ZoneAgent:
		m.Components.Agent = nil
	case core.ZoneControl:
		m.Components.Control = nil
	}
}

func TestAManifestMissingAnyZoneIsRejected(t *testing.T) {
	for _, zone := range core.Zones() {
		for mode, valid := range map[string]func(*testing.T) *manifest.Manifest{"docker": docker, "standalone": standalone} {
			t.Run(fmt.Sprintf("%s/%s", zone, mode), func(t *testing.T) {
				m := valid(t)
				dropZone(m, zone)
				report := Check(m)
				wantOnly(t, report, RuleZoneMissing, "components."+string(zone))
				if len(report.Findings) != 1 {
					t.Fatalf("one missing zone is one finding, got %+v", report.Findings)
				}
				finding := report.Findings[0]
				if !strings.Contains(finding.Message, fmt.Sprintf("Zone %d", zone.Number())) || !strings.Contains(finding.Fix, "components."+string(zone)) {
					t.Errorf("the finding must name the zone and the key to add, got %+v", finding)
				}
			})
		}
	}
}

func TestEveryMissingZoneIsReported(t *testing.T) {
	m := docker(t)
	for _, zone := range core.Zones() {
		dropZone(m, zone)
	}
	report := Check(m)
	for _, zone := range core.Zones() {
		wantOnly(t, report, RuleZoneMissing, "components."+string(zone))
	}
	if len(report.Findings) != 3 {
		t.Fatalf("three missing zones are three findings, got %+v", report.Findings)
	}
}

// Three deployments of one zone each are not one factory deployment: a
// manifest holding only one zone's units lacks the other zones' units too.
func TestAManifestMissingAnyRuntimeUnitIsRejected(t *testing.T) {
	for _, name := range manifest.UnitNames() {
		t.Run(string(name), func(t *testing.T) {
			m := docker(t)
			delete(m.Units, name)
			report := Check(m)
			if !report.Has(RuleUnitMissing) {
				t.Fatalf("a manifest without unit %q must be rejected, got %+v", name, report.Findings)
			}
			for _, finding := range report.Findings {
				// Removing the supervisor unit also leaves its trust volume unheld.
				if finding.Rule == RuleUnitMissing && finding.Path == "units."+string(name) {
					return
				}
			}
			t.Fatalf("no finding at units.%s, got %+v", name, report.Findings)
		})
	}
}

func TestAUnitKeepsTheLifecycleOfItsName(t *testing.T) {
	cases := map[manifest.UnitName]manifest.Lifecycle{
		manifest.UnitRunner:   manifest.LifecycleLongRunning, // a runner lives for one run
		manifest.UnitQARunner: manifest.LifecycleDurable,
		manifest.UnitState:    manifest.LifecycleEphemeral, // state outlives every release
		manifest.UnitApp:      manifest.LifecycleEphemeral,
	}
	for name, wrong := range cases {
		m := docker(t)
		editUnit(m, name, func(u *manifest.Unit) { u.Lifecycle = wrong })
		wantOnly(t, Check(m), RuleUnitLifecycle, fmt.Sprintf("units.%s.lifecycle", name))
	}
	// A lifecycle left out is reported once, by the rule and not also by the schema.
	m := docker(t)
	editUnit(m, manifest.UnitRunner, func(u *manifest.Unit) { u.Lifecycle = "" })
	if report := Check(m); len(report.Findings) != 1 {
		t.Fatalf("want one finding for an undeclared lifecycle, got %+v", report.Findings)
	}
}

func TestAManifestMissingAnyNetworkSegmentIsRejected(t *testing.T) {
	for _, name := range manifest.NetworkNames() {
		m := docker(t)
		delete(m.Networks, name)
		wantOnly(t, Check(m), RuleNetworkMissing, "networks."+string(name))
	}
}

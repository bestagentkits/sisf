package conformance

import (
	"fmt"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

func TestEveryUnitStaysOnTheSegmentsItsRoleAllows(t *testing.T) {
	t.Parallel()
	want := map[manifest.UnitName][]manifest.NetworkName{
		manifest.UnitApp:        {manifest.NetworkApp},
		manifest.UnitControl:    {manifest.NetworkControl, manifest.NetworkFactory},
		manifest.UnitSupervisor: {manifest.NetworkControl},
		manifest.UnitDispatcher: {manifest.NetworkFactory},
		manifest.UnitRunner:     {manifest.NetworkSandbox},
		manifest.UnitQARunner:   {manifest.NetworkSandbox},
		manifest.UnitState:      {manifest.NetworkApp, manifest.NetworkFactory, manifest.NetworkControl},
		manifest.UnitArtifacts:  {manifest.NetworkApp, manifest.NetworkFactory, manifest.NetworkControl},
		manifest.UnitEdge:       {manifest.NetworkApp, manifest.NetworkControl, manifest.NetworkSandbox},
	}
	for _, unit := range manifest.UnitNames() {
		allowed, constrained := want[unit]
		if !constrained || !slices.Equal(allowedSegments[unit], allowed) {
			t.Fatalf("unit %q must have the allowed-segment set %v, got %v", unit, allowed, allowedSegments[unit])
		}
		for _, segment := range manifest.NetworkNames() {
			m := docker(t)
			editUnit(m, unit, func(u *manifest.Unit) {
				if !slices.Contains(u.Networks, segment) {
					u.Networks = append(u.Networks, segment)
				}
			})
			report := Check(m)
			if slices.Contains(allowed, segment) {
				if !report.Conformant() {
					t.Errorf("%s may attach to %s, got %+v", unit, segment, report.Findings)
				}
				continue
			}
			wantOnly(t, report, RuleNetworkSegment, fmt.Sprintf("units.%s.networks", unit))
		}
	}
}

func TestAUnitOnEverySegmentIsRejectedOncePerSegmentItMayNotJoin(t *testing.T) {
	t.Parallel()
	m := docker(t)
	editUnit(m, manifest.UnitDispatcher, func(u *manifest.Unit) { u.Networks = manifest.NetworkNames() })
	report := Check(m)
	wantOnly(t, report, RuleNetworkSegment, "units.dispatcher.networks")
	if len(report.Findings) != 3 {
		t.Fatalf("a dispatcher on all four segments joins three it may not, got %+v", report.Findings)
	}
}

// A unit attached to no segment is placed on the platform's default network,
// which the four segments do not divide.
func TestAUnitThatRunsIsAttachedToASegment(t *testing.T) {
	t.Parallel()
	running := []manifest.UnitName{manifest.UnitApp, manifest.UnitControl, manifest.UnitSupervisor, manifest.UnitDispatcher,
		manifest.UnitRunner, manifest.UnitQARunner, manifest.UnitState}
	for _, unit := range running {
		m := docker(t)
		editUnit(m, unit, func(u *manifest.Unit) { u.Networks = nil })
		wantOnly(t, Check(m), RuleNetworkUnattached, fmt.Sprintf("units.%s.networks", unit))
	}
	// The edge is also the sandbox's way out, which it then no longer is.
	m := docker(t)
	editUnit(m, manifest.UnitEdge, func(u *manifest.Unit) { u.Networks = nil })
	if report := Check(m); !report.Has(RuleNetworkUnattached) || !report.Has(RuleEgressProxy) {
		t.Fatalf("an unattached edge is on no segment and proxies none, got %+v", report.Findings)
	}
	// Plain storage runs nothing, so it needs no segment.
	for _, m := range []*manifest.Manifest{docker(t), standalone(t)} {
		storage := m.Units[manifest.UnitArtifacts]
		if storage.Image != "" || len(storage.Command) != 0 || len(storage.Networks) != 0 {
			t.Fatalf("the %s example must keep its artifacts as plain storage, got %+v", m.Mode, storage)
		}
	}
	// Once it runs an image, it is a process like any other.
	m = docker(t)
	editUnit(m, manifest.UnitArtifacts, func(u *manifest.Unit) {
		u.Image, u.Digest = "ghcr.io/acme/object-store", m.Units[manifest.UnitEdge].Digest
	})
	wantOnly(t, Check(m), RuleNetworkUnattached, "units.artifacts.networks")
}

func TestTheSandboxReachesTheOutsideOnlyThroughADeclaredEgressProxy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		sandbox manifest.Network
		rule    Rule
		path    string
	}{
		{"the sandbox has a route of its own to the outside", manifest.Network{EgressVia: "edge"}, RuleSandboxNotInternal, "networks.sandbox.internal"},
		{"the sandbox names no egress proxy", manifest.Network{Internal: true}, RuleEgressProxy, "networks.sandbox.egress_via"},
		{"the egress proxy is not a runtime unit", manifest.Network{Internal: true, EgressVia: "squid"}, RuleEgressProxy, "networks.sandbox.egress_via"},
		{"the egress proxy is a runner", manifest.Network{Internal: true, EgressVia: "runner"}, RuleEgressProxy, "networks.sandbox.egress_via"},
		{"the egress proxy is the QA runner", manifest.Network{Internal: true, EgressVia: "qa-runner"}, RuleEgressProxy, "networks.sandbox.egress_via"},
		{"the egress proxy is not on the sandbox segment", manifest.Network{Internal: true, EgressVia: "control"}, RuleEgressProxy, "networks.sandbox.egress_via"},
	}
	for _, tc := range cases {
		for mode, valid := range map[string]func(*testing.T) *manifest.Manifest{"docker": docker, "standalone": standalone} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				m := valid(t)
				m.Networks[manifest.NetworkSandbox] = tc.sandbox
				wantOnly(t, Check(m), tc.rule, tc.path)
			})
		}
	}

	// An empty sandbox declaration is open and has no proxy: both are said.
	m := docker(t)
	m.Networks[manifest.NetworkSandbox] = manifest.Network{}
	if report := Check(m); !report.Has(RuleSandboxNotInternal) || !report.Has(RuleEgressProxy) || len(report.Findings) != 2 {
		t.Fatalf("want the open segment and the missing proxy reported, got %+v", report.Findings)
	}

	// The proxy has to be reachable from the segment it serves.
	m = docker(t)
	editUnit(m, manifest.UnitEdge, func(u *manifest.Unit) {
		u.Networks = []manifest.NetworkName{manifest.NetworkApp, manifest.NetworkControl}
	})
	wantOnly(t, Check(m), RuleEgressProxy, "networks.sandbox.egress_via")

	// A way out named on any other segment is held to the same rule.
	m = docker(t)
	m.Networks[manifest.NetworkFactory] = manifest.Network{Internal: true, EgressVia: "squid"}
	wantOnly(t, Check(m), RuleEgressProxy, "networks.factory.egress_via")
}

func TestOnlyTheApplicationTheEdgeAndNamedControlRoutesFaceTheOutside(t *testing.T) {
	t.Parallel()
	hidden := []manifest.UnitName{manifest.UnitSupervisor, manifest.UnitDispatcher, manifest.UnitRunner, manifest.UnitQARunner,
		manifest.UnitState, manifest.UnitArtifacts}
	for _, unit := range hidden {
		for mode, valid := range map[string]func(*testing.T) *manifest.Manifest{"docker": docker, "standalone": standalone} {
			t.Run(string(unit)+"/"+mode, func(t *testing.T) {
				m := valid(t)
				editUnit(m, unit, func(u *manifest.Unit) { u.Public = true })
				wantOnly(t, Check(m), RulePublicExposure, fmt.Sprintf("units.%s.public", unit))
			})
		}
	}
	// Naming routes does not open a unit that may not be public at all.
	m := docker(t)
	editUnit(m, manifest.UnitRunner, func(u *manifest.Unit) { u.Public, u.PublicRoutes = true, []string{"/status"} })
	wantOnly(t, Check(m), RulePublicExposure, "units.runner.public")

	whole := map[string][]string{
		"units.control.public_routes":    nil,
		"units.control.public_routes[0]": {"/"},
		"units.control.public_routes[1]": {"/webhooks/github", ""},
		"units.control.public_routes[2]": {"/webhooks/github", "/report", "dashboard"},
	}
	for path, routes := range whole {
		m := docker(t)
		editUnit(m, manifest.UnitControl, func(u *manifest.Unit) { u.Public, u.PublicRoutes = true, routes })
		wantOnly(t, Check(m), RulePublicExposure, path)
	}

	m = docker(t)
	editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Public = true })
	editUnit(m, manifest.UnitControl, func(u *manifest.Unit) { u.Public, u.PublicRoutes = true, []string{"/webhooks/github", "/report"} })
	if report := Check(m); !report.Conformant() || !m.Units[manifest.UnitEdge].Public {
		t.Fatalf("the application, the edge and named control routes may face the outside, got %+v", report.Findings)
	}
}

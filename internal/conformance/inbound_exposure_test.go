package conformance

import (
	"fmt"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// A run accepts no inbound connection, so nothing listens where agent code
// executes.
func TestARunnerDeclaresNoPort(t *testing.T) {
	t.Parallel()
	for _, unit := range []manifest.UnitName{manifest.UnitRunner, manifest.UnitQARunner} {
		for mode, valid := range bothModes {
			t.Run(string(unit)+"/"+mode, func(t *testing.T) {
				t.Parallel()
				m := valid(t)
				editUnit(m, unit, func(u *manifest.Unit) { u.Port = 9000 })
				report := Check(m)
				wantOnly(t, report, RuleRunnerInbound, fmt.Sprintf("units.%s.port", unit))
				if len(report.Findings) != 1 {
					t.Fatalf("one port is one finding, got %+v", report.Findings)
				}
			})
		}
	}
}

// Routes are read for the control unit alone. On another unit they restrict
// nothing, so they are refused instead of silently ignored.
func TestPublicRoutesAreNamedOnTheControlUnitAlone(t *testing.T) {
	t.Parallel()
	for _, unit := range []manifest.UnitName{manifest.UnitApp, manifest.UnitEdge, manifest.UnitDispatcher, manifest.UnitState} {
		t.Run(string(unit), func(t *testing.T) {
			t.Parallel()
			m := docker(t)
			editUnit(m, unit, func(u *manifest.Unit) { u.PublicRoutes = []string{"/admin"} })
			report := Check(m)
			wantOnly(t, report, RulePublicExposure, fmt.Sprintf("units.%s.public_routes", unit))
			if len(report.Findings) != 1 {
				t.Fatalf("one list of ignored routes is one finding, got %+v", report.Findings)
			}
		})
	}
}

func TestAPublicRouteOfTheControlUnitNamesOnePath(t *testing.T) {
	t.Parallel()
	patterns := []string{"/*", "/.", "/..", "/webhooks/*", "/webhooks/../", "/a//b", "/report/", "//", "/{id}", "/:id",
		"/report?debug=1", "/report#top", "/a b", "/%2e%2e", "/**", "/[a-z]"}
	for _, route := range patterns {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			m := docker(t)
			editUnit(m, manifest.UnitControl, func(u *manifest.Unit) { u.Public, u.PublicRoutes = true, []string{"/webhooks/github", route} })
			report := Check(m)
			wantOnly(t, report, RulePublicExposure, "units.control.public_routes[1]")
			if len(report.Findings) != 1 {
				t.Fatalf("one route that is not one path is one finding, got %+v", report.Findings)
			}
		})
	}
	named := []string{"/webhooks/github", "/report", "/.well-known/sisf", "/api/v1.2/report_x-y~z"}
	m := docker(t)
	editUnit(m, manifest.UnitControl, func(u *manifest.Unit) { u.Public, u.PublicRoutes = true, named })
	if report := Check(m); !report.Conformant() {
		t.Fatalf("plain routes below the root must be accepted, got %+v", report.Findings)
	}
}

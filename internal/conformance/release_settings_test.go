package conformance

import (
	"fmt"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

var bothModes = map[string]func(*testing.T) *manifest.Manifest{"docker": docker, "standalone": standalone}

// The release manifest pins the digest of every unit. Only plain storage,
// which runs nothing, and a supervisor unit pinned by the supervisor block
// have none of their own.
func TestEveryUnitThatRunsPinsItsDigest(t *testing.T) {
	t.Parallel()
	pinned := []manifest.UnitName{manifest.UnitApp, manifest.UnitControl, manifest.UnitDispatcher, manifest.UnitRunner,
		manifest.UnitQARunner, manifest.UnitEdge}
	for _, unit := range pinned {
		for mode, valid := range bothModes {
			for _, digest := range []core.Digest{"", "latest", "sha256:abc"} {
				t.Run(fmt.Sprintf("%s/%s/%q", unit, mode, digest), func(t *testing.T) {
					m := valid(t)
					editUnit(m, unit, func(u *manifest.Unit) { u.Digest = digest })
					wantOnly(t, Check(m), RuleUnitNotPinned, fmt.Sprintf("units.%s.digest", unit))
				})
			}
		}
	}

	// A durable unit that runs an image is an artifact like any other.
	m := docker(t)
	editUnit(m, manifest.UnitState, func(u *manifest.Unit) { u.Digest = "" })
	wantOnly(t, Check(m), RuleUnitNotPinned, "units.state.digest")

	// One that names neither an image nor a command is storage: nothing to pin.
	for mode, valid := range bothModes {
		m := valid(t)
		if storage := m.Units[manifest.UnitArtifacts]; storage.Image != "" || len(storage.Command) != 0 || storage.Digest != "" {
			t.Fatalf("%s: the example must keep its artifacts as unpinned plain storage, got %+v", mode, storage)
		}
	}
	// Naming a command does not make a process unit storage.
	m = standalone(t)
	editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Image, u.Command, u.Digest = "", nil, "" })
	wantOnly(t, Check(m), RuleUnitNotPinned, "units.app.digest")

	// The supervisor block pins the supervisor unit; without the block the
	// unit pins itself.
	m = docker(t)
	editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Digest = "" })
	if report := Check(m); !report.Conformant() {
		t.Fatalf("a supervisor unit pinned by the supervisor block needs no digest of its own, got %+v", report.Findings)
	}
	m.Supervisor, m.Release.Units = nil, []manifest.UnitName{}
	wantOnly(t, Check(m), RuleUnitNotPinned, "units.supervisor.digest")
}

func TestAReleaseIsObservedOverAPositiveSpan(t *testing.T) {
	t.Parallel()
	spans := map[string]func(r *manifest.Release){
		"release.rollback.window":      func(r *manifest.Release) { r.Rollback.Window = 0 },
		"release.low_traffic.min_hold": func(r *manifest.Release) { r.LowTraffic.MinHold = 0 },
	}
	for path, zero := range spans {
		for mode, valid := range bothModes {
			t.Run(path+"/"+mode, func(t *testing.T) {
				m := valid(t)
				zero(&m.Release)
				wantOnly(t, Check(m), RuleReleaseDuration, path)
			})
		}
	}
	m := docker(t)
	m.Release.Rollback.Window, m.Release.LowTraffic.MinHold = 0, 0
	if report := Check(m); len(report.Findings) != 2 {
		t.Fatalf("no window and no hold are two findings, got %+v", report.Findings)
	}
}

func TestACanaryGrowsFromAShareOfTheTraffic(t *testing.T) {
	t.Parallel()
	for _, none := range [][]int{nil, {}} {
		m := docker(t)
		m.Release.CanarySteps = none
		wantOnly(t, Check(m), RuleCanaryStepsMissing, "release.canary_steps")
	}
	degenerate := [][]int{
		{100, 5, 5},  // all traffic first, then back
		{100},        // all traffic at once is no canary
		{5, 5},       // a step that does not grow
		{5, 25, 25},  //
		{25, 5, 100}, // traffic taken back from a candidate
		{5, 100, 50}, //
	}
	for _, steps := range degenerate {
		t.Run(fmt.Sprint(steps), func(t *testing.T) {
			m := docker(t)
			m.Release.CanarySteps = steps
			report := Check(m)
			wantOnly(t, report, RuleCanarySteps, "release.canary_steps")
			if len(report.Findings) != 1 {
				t.Fatalf("one list of steps is one finding, got %+v", report.Findings)
			}
		})
	}
	for _, steps := range [][]int{{5, 25, 100}, {1, 100}, {10, 50}, {50}} {
		m := docker(t)
		m.Release.CanarySteps = steps
		if report := Check(m); !report.Conformant() {
			t.Errorf("canary steps %v grow from a share of the traffic, got %+v", steps, report.Findings)
		}
	}
	// Steps mean nothing to a release that switches all traffic at once.
	m := standalone(t)
	if m.Release.Strategy != manifest.StrategyBlueGreen || len(m.Release.CanarySteps) != 0 {
		t.Fatalf("the standalone example must be a blue-green release without steps, got %+v", m.Release)
	}
	m.Release.CanarySteps = []int{100, 5, 5}
	if report := Check(m); !report.Conformant() {
		t.Fatalf("canary steps are not read for a blue-green release, got %+v", report.Findings)
	}
}

func TestAtLeastOneKindOfStepHasAModelRoute(t *testing.T) {
	t.Parallel()
	for _, empty := range []map[core.StepKind]core.ModelRoute{nil, {}} {
		for mode, valid := range bothModes {
			m := valid(t)
			m.ModelRoutes.Routes = empty
			report := Check(m)
			wantOnly(t, report, RuleModelRoutesEmpty, "model_routes.routes")
			if len(report.Findings) != 1 {
				t.Fatalf("%s: an empty route table is one finding, got %+v", mode, report.Findings)
			}
		}
	}
}

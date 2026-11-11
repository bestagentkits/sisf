package conformance

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// On a file system that ignores letter case, as macOS does by default,
// data/Trust and data/trust are one directory. In Standalone mode every
// comparison that refuses a shared directory treats them as one.
func TestInStandaloneModeDirectoriesThatDifferOnlyByCaseAreOneDirectory(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		change func(m *manifest.Manifest)
		rule   Rule
		path   string
	}{
		{"the runner's workspace inside the trust directory spelled in capitals",
			func(m *manifest.Manifest) { remount(m, manifest.UnitRunner, "data/Trust/ws") },
			RuleTrustVolumeExposed, "units.runner.mounts[0].path"},
		{"the runner's workspace on Zone 3's directory spelled in capitals",
			func(m *manifest.Manifest) { remount(m, manifest.UnitRunner, "Data/Control") },
			RuleVolumePathOverlap, "units.runner.mounts[0].path"},
		{"a binary in the coder's workspace spelled in capitals",
			func(m *manifest.Manifest) {
				editUnit(m, manifest.UnitEdge, func(u *manifest.Unit) { u.Command = []string{"DATA/workspaces/edge"} })
			},
			RuleExecutableInAnotherUnitsDirectory, "units.edge.command"},
		{"the supervisor's binary in the application's data spelled in capitals",
			func(m *manifest.Manifest) {
				m.Supervisor.Image = "data/APP/sisf-supervisor"
				editUnit(m, manifest.UnitSupervisor, func(u *manifest.Unit) { u.Image, u.Command = "", nil })
			},
			RuleSupervisorNotPinned, "supervisor.image"},
		{"the state store on the trust volume spelled in capitals",
			func(m *manifest.Manifest) { m.Storage.State.Path = "Data/Trust/factory.db" },
			RuleTrustVolumeStorage, "storage.state.path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := standalone(t)
			tc.change(m)
			wantOnly(t, Check(m), tc.rule, tc.path)
		})
	}

	// The two workspaces differ only by case: QA would read what the coder writes.
	m := standalone(t)
	remount(m, manifest.UnitQARunner, "data/Workspaces")
	if report := Check(m); !report.reportedAt(RuleQASharedWorkspace, "units.qa-runner.mounts[0]") {
		t.Fatalf("a QA workspace that is the coder's workspace in other letters must be refused, got %+v", report.Findings)
	}

	// A path that must lie inside a unit's volume is still compared exactly:
	// on a file system that tells case apart, Data/State is not data/state.
	m = standalone(t)
	m.Storage.State.Path = "Data/State/factory.db"
	wantOnly(t, Check(m), RuleStatePlacement, "storage.state.path")

	// In Docker mode a mount path is a place inside one container.
	m = docker(t)
	remount(m, manifest.UnitApp, "/VAR/lib/sisf/trust")
	if report := Check(m); !report.Conformant() {
		t.Fatalf("container paths are not compared as host directories, got %+v", report.Findings)
	}
}

func TestFoldingCaseMakesOneSpellingOfEveryCaseVariant(t *testing.T) {
	t.Parallel()
	same := [][2]string{{"data/Trust", "data/trust"}, {"DATA/TRUST", "data/trust"}, {"STRA\u1e9eE", "straße"}, {"ΣΊΣΥΦΟΣ", "σίσυφος"}, {"K", "\u212a"}}
	for _, pair := range same {
		if foldCase(pair[0]) != foldCase(pair[1]) {
			t.Errorf("foldCase(%q) = %q, foldCase(%q) = %q; want one spelling", pair[0], foldCase(pair[0]), pair[1], foldCase(pair[1]))
		}
	}
	if foldCase("data/trust") == foldCase("data/trusts") {
		t.Error("folding case must not merge different names")
	}
}

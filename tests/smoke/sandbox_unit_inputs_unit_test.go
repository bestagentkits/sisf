//go:build smoke

package smoke

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The inputs the install declares for the sandbox admit what both rehearsal
// candidates change, the good one's app.go and the bad one's fault file,
// and leave the sandbox's go.mod and CI to the Owner. They conflict with no
// protected path and no other unit's inputs.
func TestTheSandboxInputsAdmitBothRehearsalCandidates(t *testing.T) {
	declared := map[string][]string{string(manifest.UnitApp): sandboxAppInputs}
	inputs, err := release.NewUnitInputs(declared)
	if err != nil {
		t.Fatal(err)
	}
	protected := release.DefaultProtectedPaths()
	for _, changed := range [][]string{{rehearsalPage}, {rehearsalFaultFile}, {rehearsalPage, rehearsalFaultFile}} {
		if err := release.CheckSelfPublishedChange(protected, inputs, "app", changed); err != nil {
			t.Errorf("a rehearsal candidate changing %v = %v", changed, err)
		}
	}
	for _, p := range []string{"go.mod", ".github/workflows/ci.yml", "synthetic/main.go"} {
		if err := release.CheckSelfPublishedChange(protected, inputs, "app", []string{rehearsalPage, p}); !core.HasCode(err, core.CodeApprovalRequired) {
			t.Errorf("a candidate also changing %s = %v, want APPROVAL_REQUIRED", p, err)
		}
	}
	if conflicts := release.UnitInputConflicts(declared, protected); len(conflicts) != 0 {
		t.Fatalf("the sandbox inputs conflict: %+v", conflicts)
	}
}

// declareSandboxInputs writes the inputs into the install's factory.yaml
// and keeps everything else the install wrote.
func TestTheInstallDeclaresTheSandboxInputs(t *testing.T) {
	home := t.TempDir()
	layout := install.Layout{Dir: home}
	raw, err := os.ReadFile(filepath.Join(repoRoot(), ".sisf", "factory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(layout.Config()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.Config(), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := layout.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if err := declareSandboxInputs(home); err != nil {
		t.Fatal(err)
	}
	after, err := layout.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Release.UnitInputs[manifest.UnitApp]; !slices.Equal(got, sandboxAppInputs) {
		t.Fatalf("release.unit_inputs.app = %v, want %v", got, sandboxAppInputs)
	}
	if after.FactoryID != before.FactoryID || !slices.Equal(after.Release.Units, before.Release.Units) {
		t.Fatalf("the install's manifest changed beyond its inputs: %+v", after.Release)
	}
}

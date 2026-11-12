package install

import (
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/internal/conformance"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The manifest init renders, with the application's inputs declared, is
// written and read back with them and stays conformant in both modes; a
// manifest that departs from the schema is not written.
func TestAnInstallsManifestKeepsDeclaredUnitInputs(t *testing.T) {
	t.Parallel()
	for _, mode := range []manifest.Mode{manifest.ModeStandalone, manifest.ModeDocker} {
		o, err := testOptions(mode).normalize()
		if err != nil {
			t.Fatal(err)
		}
		layout := Layout{Dir: t.TempDir()}
		m := buildManifest(o)
		m.Release.UnitInputs = map[manifest.UnitName][]string{manifest.UnitApp: {"*.go"}}
		if err := layout.SaveManifest(m); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		back, err := layout.LoadManifest()
		if err != nil {
			t.Fatal(err)
		}
		if got := back.Release.UnitInputs[manifest.UnitApp]; !slices.Equal(got, []string{"*.go"}) {
			t.Fatalf("%s: release.unit_inputs.app read back as %v", mode, got)
		}
		if err := conformance.Check(back).Err(); err != nil {
			t.Errorf("%s: the manifest with declared inputs is not conformant: %v", mode, err)
		}
		m.Release.UnitInputs[manifest.UnitApp] = []string{"**"}
		if err := layout.SaveManifest(m); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Fatalf("%s: a manifest with an unusable pattern was written: %v", mode, err)
		}
	}
}

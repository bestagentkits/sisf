package install

import (
	"os"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The simulation's manifest names real agent backends; it says that the
// simulation runs none of them, so nobody expects a model bill from it.
func TestTheSimulationManifestSaysItRunsNoRealAgent(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeStandalone)
	raw, err := os.ReadFile(in.Layout.Config())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "The simulation does not run") {
		t.Fatalf("the simulation manifest does not say what runs:\n%s", raw)
	}
	if _, err := manifest.Parse(raw); err != nil {
		t.Fatalf("the note broke the manifest: %v", err)
	}
}

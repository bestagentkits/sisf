//go:build smoke

package smoke

import (
	"slices"

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// sandboxAppInputs are the inputs of the sandbox repository's application,
// as the install declares them in release.unit_inputs: its program is the
// Go files at the repository root (app.go, faults.go and what a candidate
// adds there, such as the rehearsal's release_rehearsal_fault.go). Its
// go.mod, its synthetic suite under synthetic/ and its CI are not inputs:
// a candidate changing them is left for the Owner.
var sandboxAppInputs = []string{"*.go"}

// declareSandboxInputs writes the sandbox application's inputs into the
// install's factory.yaml, the manifest the supervisor is started with. The
// built-in inputs describe this repository's layout, not the sandbox's.
func declareSandboxInputs(home string) error {
	layout := install.Layout{Dir: home}
	m, err := layout.LoadManifest()
	if err != nil {
		return err
	}
	if m.Release.UnitInputs == nil {
		m.Release.UnitInputs = map[manifest.UnitName][]string{}
	}
	m.Release.UnitInputs[manifest.UnitApp] = slices.Clone(sandboxAppInputs)
	return layout.SaveManifest(m)
}

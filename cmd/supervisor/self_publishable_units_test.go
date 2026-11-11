package main

import (
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func TestThePipelinePublishesOnlyTheUnitsTheManifestNames(t *testing.T) {
	m := &manifest.Manifest{Release: manifest.Release{Units: []manifest.UnitName{manifest.UnitApp}}}
	if got := selfPublishableUnits(m); !slices.Equal(got, []string{"app"}) {
		t.Fatalf("units = %v, want only app", got)
	}
}

func TestAManifestNamingNoUnitPublishesNothing(t *testing.T) {
	got := selfPublishableUnits(&manifest.Manifest{})
	if got == nil {
		t.Fatal("a nil list would fall back to the pipeline's default units")
	}
	if len(got) != 0 {
		t.Fatalf("units = %v, want none", got)
	}
	if len(release.DefaultSelfPublishable()) == 0 {
		t.Fatal("the default list is empty, so this test proves nothing")
	}
}

// The inputs the installed manifest declares are the ones the release
// judges by: a declared unit's built-in inputs are replaced.
func TestTheDeclaredUnitInputsComeFromTheInstalledManifest(t *testing.T) {
	m := &manifest.Manifest{Release: manifest.Release{Units: []manifest.UnitName{manifest.UnitApp},
		UnitInputs: map[manifest.UnitName][]string{manifest.UnitApp: {"*.go"}}}}
	inputs, err := release.NewUnitInputs(declaredUnitInputs(m))
	if err != nil {
		t.Fatal(err)
	}
	if unit, ok := inputs.UnitOf("app.go"); !ok || unit != "app" {
		t.Fatalf("app.go is the input of %q (%v), want app", unit, ok)
	}
	if _, ok := inputs.UnitOf("examples/hello-app/main.go"); ok {
		t.Fatal("the built-in input of the declared unit is still an input")
	}
	if got := declaredUnitInputs(&manifest.Manifest{}); len(got) != 0 {
		t.Fatalf("a manifest declaring nothing declares %v", got)
	}
}

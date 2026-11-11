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

package main

import (
	"slices"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// selfPublishableUnits returns the units the manifest's release.units lets
// the pipeline publish by itself. The result is never nil: a manifest that
// names no unit publishes nothing, rather than falling back to the
// pipeline's default list.
func selfPublishableUnits(m *manifest.Manifest) []string {
	units := make([]string, 0, len(m.Release.Units))
	for _, u := range m.Release.Units {
		units = append(units, string(u))
	}
	return units
}

// declaredUnitInputs returns the inputs the installed manifest's
// release.unit_inputs declares, per unit. The supervisor reads them only
// from the manifest it was started with (--factory-manifest), never from a
// candidate's tree: a candidate's .sisf/factory.yaml is a protected path.
func declaredUnitInputs(m *manifest.Manifest) map[string][]string {
	declared := make(map[string][]string, len(m.Release.UnitInputs))
	for unit, patterns := range m.Release.UnitInputs {
		declared[string(unit)] = slices.Clone(patterns)
	}
	return declared
}

package main

import "github.com/bestagentkits/sisf/pkg/manifest"

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

package main

import (
	"slices"
	"testing"
)

func TestApproveMigrationsTakesDistinctPositiveVersions(t *testing.T) {
	t.Parallel()
	for value, want := range map[string][]int{"": nil, "12": {12}, "12, 14": {12, 14}} {
		got, err := parseMigrationVersions(value)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%q = %v, %v; want %v", value, got, err, want)
		}
	}
	for _, value := range []string{"twelve", "0", "-3", "12,", "12,12"} {
		if _, err := parseMigrationVersions(value); err == nil {
			t.Errorf("%q was accepted", value)
		}
	}
}

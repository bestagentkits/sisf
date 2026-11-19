package main

import (
	"strconv"
	"strings"
)

// parseMigrationVersions reads the comma-separated versions of
// --approve-migrations. An empty value approves nothing; a version that is
// not a positive number, or one named twice, is a usage error.
func parseMigrationVersions(value string) ([]int, error) {
	if value == "" {
		return nil, nil
	}
	var versions []int
	seen := map[int]bool{}
	for _, part := range strings.Split(value, ",") {
		v, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || v < 1 {
			return nil, usagef("--approve-migrations takes migration versions such as 12 or 12,14, not %q", part)
		}
		if seen[v] {
			return nil, usagef("--approve-migrations names migration %d twice", v)
		}
		seen[v] = true
		versions = append(versions, v)
	}
	return versions, nil
}

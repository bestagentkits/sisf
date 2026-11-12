package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVersionNamesTheBuild(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		status, stdout, _ := runWithOutput(t, args...)
		if status != exitOK || !strings.HasPrefix(stdout, "sisf ") || !strings.Contains(stdout, "commit ") {
			t.Errorf("%v = %d %q", args, status, stdout)
		}
	}
	status, stdout, _ := runWithOutput(t, "version", "--json")
	var v versionInfo
	if status != exitOK || json.Unmarshal([]byte(stdout), &v) != nil || v.Version == "" || v.GoVersion == "" {
		t.Fatalf("version --json = %d %q", status, stdout)
	}
}

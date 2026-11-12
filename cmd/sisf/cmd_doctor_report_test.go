package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorReportsAFailureOnceAndCanSpeakJSON(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "none")
	status, stdout, stderr := runWithOutput(t, "doctor", "--dir", dir)
	if status != exitFailure || stderr != "" || !strings.HasPrefix(stdout, "sisf ") {
		t.Fatalf("doctor = %d\nstdout: %s\nstderr: %q, want the report once, on stdout", status, stdout, stderr)
	}
	status, stdout, _ = runWithOutput(t, "doctor", "--dir", dir, "--json")
	var report struct {
		OK     bool `json:"ok"`
		Counts struct {
			Fail int `json:"fail"`
		} `json:"counts"`
		Checks []struct {
			Name string `json:"name"`
		} `json:"checks"`
	}
	if status != exitFailure || json.Unmarshal([]byte(stdout), &report) != nil || report.OK || report.Counts.Fail == 0 || len(report.Checks) == 0 {
		t.Fatalf("doctor --json = %d %q", status, stdout)
	}
}

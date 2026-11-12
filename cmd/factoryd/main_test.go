package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func runWith(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	status := run(context.Background(), args, func(k string) string { return env[k] }, &stdout, &stderr)
	return status, stdout.String(), stderr.String()
}

func TestAWrongCommandLineExitsWithTheUsageStatus(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		nil,
		{"build"},
		{"policy"},
		{"policy", "approve"},
		{"policy", "request"},
		{"policy", "request", "--revision", "abc", "extra"},
		{"issue", "retry", "--issue", "not-an-issue"},
		{"dispatcher", "--fake-agent-work", "-1s"},
		{"control", "extra"},
		{"report", "--token", "many"},
	} {
		status, _, stderr := runWith(t, nil, args...)
		if status != exitUsage || !strings.Contains(stderr, "usage:") {
			t.Errorf("%q: status %d, stderr %q", args, status, stderr)
		}
	}
	if status, stdout, _ := runWith(t, nil, "--help"); status != exitOK || !strings.Contains(stdout, "factoryd control") {
		t.Errorf("help: status %d, %q", status, stdout)
	}
}

func TestAMissingEnvironmentFailsWithoutPrintingASecret(t *testing.T) {
	t.Parallel()
	const secret = "a-secret-token-value"
	env := map[string]string{"SISF_OWNER_TOKEN": secret, "SISF_WEBHOOK_SECRET": secret, "SISF_GITHUB_TOKEN": secret}
	status, stdout, stderr := runWith(t, env, "control")
	if status != exitFailure || !strings.Contains(stderr, "SISF_CONFIG is not set") {
		t.Fatalf("status %d, stderr %q", status, stderr)
	}
	env["SISF_CONFIG"] = filepath.Join(t.TempDir(), "missing.yaml")
	env["SISF_STATE_DSN"] = secret
	status, stdout2, stderr2 := runWith(t, env, "dispatcher")
	if status != exitFailure {
		t.Fatalf("a missing manifest did not fail: %d %q", status, stderr2)
	}
	for _, out := range []string{stdout, stderr, stdout2, stderr2} {
		if strings.Contains(out, secret) {
			t.Fatalf("a secret was printed: %q", out)
		}
	}
}

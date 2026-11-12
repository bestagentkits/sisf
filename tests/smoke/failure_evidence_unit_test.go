//go:build smoke

package smoke

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A failed start is diagnosable from the run's output alone: the evidence
// holds the end of `sisf up`'s output and of every unit's log, and nothing
// the scrubber knows as a secret.
func TestFailureEvidenceHoldsEveryUnitLogRedacted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home := filepath.Join(root, "factory")
	logs := filepath.Join(home, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := "unit-log-secret-" + strings.Repeat("q", 12)
	upLog := filepath.Join(root, "up.log")
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(upLog, "control exited (exit status 1); starting it again\n")
	write(filepath.Join(logs, "control.log"), "factoryd: INVALID_ARGUMENT: refused\n")
	write(filepath.Join(logs, "supervisor.log"), "supervisor: token "+secret+"\n")

	got := strings.Join(failureEvidence(upLog, home, newSecretScrubber(nil, secret)), "\n")
	for _, want := range []string{"sisf up:", "control exited", "control.log:", "INVALID_ARGUMENT: refused", "supervisor.log:"} {
		if !strings.Contains(got, want) {
			t.Errorf("the evidence lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, secret) {
		t.Errorf("the evidence holds a known secret:\n%s", got)
	}
}

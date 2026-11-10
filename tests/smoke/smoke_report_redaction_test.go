//go:build smoke

package smoke

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/redact"
)

func TestReportWriterRedactsTokensAndKnownSecrets(t *testing.T) {
	t.Parallel()
	// Built at run time so no credential-shaped literal sits in the source.
	tokenLooking := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	known := "plain-known-secret-" + strings.Repeat("x", 12)
	fromEnv := "value-of-an-env-token-" + strings.Repeat("z", 10)

	s := newSecretScrubber([]string{"SMOKE_TEST_API_TOKEN=" + fromEnv, "HOME=/home/someone"})
	s.addSecret(known)
	r := &smokeReport{
		Started: time.Now(), Outcome: "FAIL", Repo: sandboxRepo,
		Versions:   []namedValue{{Name: "gh", Value: "gh version 2 " + tokenLooking}},
		Commands:   []string{"gh issue view 1 --repo " + sandboxRepo},
		Deviations: []string{"the log said token=" + known, "a header carried " + tokenLooking},
		Evidence:   []string{"dispatcher.log:\nauth " + fromEnv},
		Teardown:   "sisf down exited 0",
	}
	path := filepath.Join(t.TempDir(), "reports", "report.md")
	if err := writeReport(path, r, s); err != nil {
		t.Fatalf("writeReport: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the written report: %v", err)
	}
	got := string(raw)
	for name, secret := range map[string]string{"token-looking value": tokenLooking, "known secret": known, "env token": fromEnv} {
		if strings.Contains(got, secret) {
			t.Errorf("the report still holds the %s", name)
		}
	}
	if !strings.Contains(got, redact.Marker) {
		t.Errorf("the report holds no redaction marker; the secrets were not replaced")
	}
	if !strings.Contains(got, "sisf down exited 0") || !strings.Contains(got, sandboxRepo) {
		t.Errorf("redaction removed ordinary text from the report")
	}
}

func TestScrubberRedactsSecretsAddedLater(t *testing.T) {
	t.Parallel()
	s := newSecretScrubber(nil)
	late := "cli-token-learned-late-" + strings.Repeat("q", 8)
	if got := s.clean("before " + late); !strings.Contains(got, late) {
		t.Fatalf("an unknown plain value was redacted before it was added: %q", got)
	}
	s.addSecret(late)
	s.addEnvFile("# comment\nSISF_WEBHOOK_SECRET=hook-secret-value-123456\nSISF_ZONE=control\n")
	got := s.clean("after " + late + " hook-secret-value-123456 control")
	if strings.Contains(got, late) || strings.Contains(got, "hook-secret-value-123456") {
		t.Fatalf("a secret added later was not redacted: %q", got)
	}
	if !strings.Contains(got, "control") {
		t.Fatalf("a non-secret env value was redacted: %q", got)
	}
}

package redact_test

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/agent/redact"
)

func TestTokenShapesAreRedactedWhereverTheyAppear(t *testing.T) {
	t.Parallel()
	secrets := []string{
		"sk-ant-api03-abcdefghijklmnop",
		"sk-proj-abcdefghijklmnopqrstuvwxyz",
		"ghp_abcdefghijklmnopqrstuvwxyz0123",
		"github_pat_11ABCDEFG0abcdefghijklmnop",
		"xoxb-1234567890-abcdefghij",
		"AKIAABCDEFGHIJKLMNOP",
		"eyJhbGciOiJIUzI1.eyJzdWIiOiIxMjM0.SflKxwRJSMeKKF2QT4fw",
		"-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkq\n-----END PRIVATE KEY-----",
	}
	for _, secret := range secrets {
		in := "before " + secret + " after"
		out := redact.Text(in)
		if strings.Contains(out, secret) || !strings.Contains(out, redact.Marker) {
			t.Errorf("%q was not redacted: %q", secret, out)
		}
	}
}

func TestAssignmentsAndBearerHeadersKeepTheirNameOnly(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		`GITHUB_TOKEN=abcdef123456`,
		`{"api_key":"abcdef123456"}`,
		`Authorization: Bearer abcdefghijklmnop123`,
		`password: hunter2hunter2`,
	} {
		out := redact.Text(in)
		for _, leaked := range []string{"abcdef123456", "abcdefghijklmnop123", "hunter2hunter2"} {
			if strings.Contains(out, leaked) {
				t.Errorf("%q leaked %q: %q", in, leaked, out)
			}
		}
	}
	if got := redact.Text("no secret here"); got != "no secret here" {
		t.Errorf("plain text changed: %q", got)
	}
}

func TestExactEnvironmentValuesAreRedactedEvenWithoutAKnownShape(t *testing.T) {
	t.Parallel()
	r := redact.New([]string{"PATH=/usr/bin", "PROVIDER_ACCESS_TOKEN=opaque-value-0042", "HOME=/home/x"})
	out := r.Text(`line with opaque-value-0042 and "opaque-value-0042" and /usr/bin`)
	if strings.Contains(out, "opaque-value-0042") {
		t.Errorf("env secret leaked: %q", out)
	}
	if !strings.Contains(out, "/usr/bin") {
		t.Errorf("a non-secret variable was redacted: %q", out)
	}
	escaped := redact.New(nil, "quote\"inside-secret").Text(`{"k":"quote\"inside-secret"}`)
	if strings.Contains(escaped, "inside-secret") {
		t.Errorf("JSON-escaped secret leaked: %q", escaped)
	}
}

func TestOrdinaryVariablesAreNotTreatedAsSecrets(t *testing.T) {
	t.Parallel()
	r := redact.New([]string{"GIT_AUTHOR_NAME=Ada Lovelace", "EDITOR=/usr/bin/vim", "ANTHROPIC_AUTH_TOKEN=opaque-token-value-1"})
	out := r.Text("Ada Lovelace edited with /usr/bin/vim using opaque-token-value-1")
	if !strings.Contains(out, "Ada Lovelace") || !strings.Contains(out, "/usr/bin/vim") || strings.Contains(out, "opaque-token-value-1") {
		t.Errorf("got %q", out)
	}
}

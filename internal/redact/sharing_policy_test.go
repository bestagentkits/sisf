package redact_test

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestDefaultSharingPolicySendsWorkDataButNeverPersonalDataOrSecrets(t *testing.T) {
	t.Parallel()
	policy := redact.DefaultSharingPolicy("anthropic", "openai")
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, class := range []redact.DataClass{redact.ClassSourceCode, redact.ClassIssueText, redact.ClassLogs, redact.ClassTranscripts} {
		if err := policy.Allows("Anthropic ", class); err != nil {
			t.Errorf("%s should be shareable with a configured provider: %v", class, err)
		}
	}
	for _, class := range []redact.DataClass{redact.ClassPersonalData, redact.ClassSecrets, redact.ClassTelemetry} {
		if err := policy.Allows("anthropic", class); !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("%s must not leave by default, got %v", class, err)
		}
	}
	if err := policy.Allows("unknown-provider", redact.ClassSourceCode); !core.HasCode(err, core.CodePermissionDenied) {
		t.Errorf("a provider the policy does not name receives nothing, got %v", err)
	}
	if err := policy.Allows("anthropic", "everything"); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("an unknown class is an error, got %v", err)
	}
}

func TestPolicyThatWouldShareSecretsIsInvalid(t *testing.T) {
	t.Parallel()
	cases := map[string]redact.SharingPolicy{
		"secrets":       {Providers: map[string][]redact.DataClass{"anthropic": {redact.ClassSecrets}}},
		"unknown class": {Providers: map[string][]redact.DataClass{"anthropic": {"credentials-lite"}}},
		"blank name":    {Providers: map[string][]redact.DataClass{" ": {redact.ClassLogs}}},
		"duplicate":     {Providers: map[string][]redact.DataClass{"OpenAI": {redact.ClassLogs}, "openai": {redact.ClassLogs}}},
	}
	for name, p := range cases {
		if err := p.Validate(); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
		}
		if err := p.Allows("anthropic", redact.ClassLogs); err == nil {
			t.Errorf("%s: an invalid policy must allow nothing", name)
		}
	}
}

func TestPrepareRedactsWhatLeavesForAProvider(t *testing.T) {
	t.Parallel()
	r := newRedactor()
	text := "log: jane.doe@example.org called with sk-ant-api03-abcdefghijklmnop"

	defaults := redact.DefaultSharingPolicy("anthropic")
	out, err := defaults.Prepare(r, "anthropic", redact.ClassLogs, text)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "jane.doe@example.org") || strings.Contains(out, "sk-ant-api03") {
		t.Errorf("default sharing must strip personal data and secrets: %q", out)
	}
	if _, err := defaults.Prepare(r, "openai", redact.ClassLogs, text); !core.HasCode(err, core.CodePermissionDenied) {
		t.Errorf("an unlisted provider gets nothing, got %v", err)
	}

	withPersonal := redact.SharingPolicy{Providers: map[string][]redact.DataClass{
		"anthropic": {redact.ClassLogs, redact.ClassPersonalData},
	}}
	out, err = withPersonal.Prepare(r, "anthropic", redact.ClassLogs, text)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "jane.doe@example.org") || strings.Contains(out, "sk-ant-api03") {
		t.Errorf("an Owner who allows personal data still never sends secrets: %q", out)
	}
	if _, err := withPersonal.Prepare(nil, "anthropic", redact.ClassLogs, text); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("preparing without a redactor must fail closed, got %v", err)
	}
}

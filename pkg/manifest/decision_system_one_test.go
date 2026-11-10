package manifest_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// systemOneSection is a valid System One section in the YAML of the file.
const systemOneSection = `
decision:
  system_one:
    backend: jev
    endpoint: https://jev.example.com/v1/judge
    version: "2026-11-01"
    timeout: 30s
    credential: env:SISF_JEV_TOKEN
    model: {provider: anthropic, model: judge, effort: low}
`

// loadWithDecision writes the example manifest followed by section and loads it.
func loadWithDecision(t *testing.T, section string) (*manifest.Manifest, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "factory.yaml")
	if err := os.WriteFile(path, []byte(exampleYAML(t)+section), 0o600); err != nil {
		t.Fatal(err)
	}
	return manifest.Load(path)
}

func TestSystemOneSectionLoadsAndRoundTrips(t *testing.T) {
	t.Parallel()
	m, err := loadWithDecision(t, systemOneSection)
	if err != nil {
		t.Fatalf("a valid System One section must load: %v", err)
	}
	got, ok := m.Decision.SystemOneConfig()
	want := manifest.SystemOne{
		Backend: manifest.SystemOneJev, Endpoint: "https://jev.example.com/v1/judge", Version: "2026-11-01",
		Timeout: manifest.Duration(30 * time.Second), Credential: "env:SISF_JEV_TOKEN",
		Model: core.ModelRoute{Provider: "anthropic", Model: "judge", Effort: "low"},
	}
	if !ok || !reflect.DeepEqual(*got, want) {
		t.Fatalf("got %+v (declared %v), want %+v", got, ok, want)
	}
	written, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	again, err := manifest.Parse(written)
	if err != nil {
		t.Fatalf("the written manifest must parse again: %v", err)
	}
	if !reflect.DeepEqual(again.Decision, m.Decision) || len(manifest.Validate(again)) != 0 {
		t.Fatalf("the section changed on the way back: %+v", again.Decision)
	}
}

func TestSystemOneSectionMayBeLeftOut(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"no section":           "",
		"an empty section":     "\ndecision: {}\n",
		"a null section":       "\ndecision:\n",
		"a null backend entry": "\ndecision: {system_one: null}\n",
	}
	for name, section := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, err := loadWithDecision(t, section)
			if err != nil {
				t.Fatalf("must load: %v", err)
			}
			if s, ok := m.Decision.SystemOneConfig(); ok || s != nil {
				t.Fatalf("no System One backend is declared, got %+v", s)
			}
		})
	}
}

func TestSystemOneEndpointMayUsePlainHTTPOnlyOnLoopback(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{"http://localhost:8080/judge", "http://127.0.0.1:8080", "http://[::1]:9000/v1", "https://clef.internal"} {
		section := strings.Replace(systemOneSection, "https://jev.example.com/v1/judge", endpoint, 1)
		if _, err := loadWithDecision(t, section); err != nil {
			t.Errorf("%s must be accepted: %v", endpoint, err)
		}
	}
}

func TestSystemOneSectionRefusesWhatItCannotTrust(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		from, to string
		// at is where the refusal is reported; refused must never be echoed.
		at, refused string
	}{
		"an unknown backend":               {"backend: jev", "backend: skynet", "decision.system_one.backend", "skynet"},
		"a floating version":               {`version: "2026-11-01"`, "version: latest", "decision.system_one.version", "latest"},
		"no version":                       {`version: "2026-11-01"`, `version: ""`, "decision.system_one.version", ""},
		"a zero timeout":                   {"timeout: 30s", "timeout: 0s", "decision.system_one.timeout", ""},
		"a timeout beyond the bound":       {"timeout: 30s", "timeout: 3m", "decision.system_one.timeout", ""},
		"an inline credential":             {"credential: env:SISF_JEV_TOKEN", "credential: sk-live-SECRETVALUE123", "decision.system_one.credential", "SECRETVALUE123"},
		"plain HTTP to a remote host":      {"https://jev.example.com", "http://jev.example.com", "decision.system_one.endpoint", "jev.example.com"},
		"a password in the endpoint":       {"https://jev.example.com", "https://judge:SECRETVALUE123@jev.example.com", "decision.system_one.endpoint", "SECRETVALUE123"},
		"a token in the endpoint query":    {"/v1/judge", "/v1/judge?token=SECRETVALUE123", "decision.system_one.endpoint", "SECRETVALUE123"},
		"a fragment in the endpoint":       {"/v1/judge", "/v1/judge#SECRETVALUE123", "decision.system_one.endpoint", "SECRETVALUE123"},
		"a relative endpoint":              {"https://jev.example.com/v1/judge", "/v1/judge", "decision.system_one.endpoint", ""},
		"no endpoint":                      {"https://jev.example.com/v1/judge", `""`, "decision.system_one.endpoint", ""},
		"a model route of white space":     {"provider: anthropic, model: judge", `provider: " ", model: judge`, "decision.system_one.model", ""},
		"a model route without a model":    {"provider: anthropic, model: judge", "provider: anthropic", "decision.system_one.model", ""},
		"a key System One does not have":   {"timeout: 30s", "timeout: 30s\n    retries: 3", "retries", ""},
		"a key the decision does not have": {"decision:", "decision:\n  system_two: {}", "system_two", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			section := strings.Replace(systemOneSection, tc.from, tc.to, 1)
			if section == systemOneSection {
				t.Fatalf("the case does not change the section")
			}
			m, err := loadWithDecision(t, section)
			if m != nil || !core.HasCode(err, core.CodeInvalidArgument) {
				t.Fatalf("want INVALID_ARGUMENT, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.at) {
				t.Errorf("the refusal must name %s, got %v", tc.at, err)
			}
			if tc.refused != "" && strings.Contains(err.Error(), tc.refused) {
				t.Errorf("the refusal repeats the refused value: %v", err)
			}
		})
	}
}

func TestValidateReportsAModelRouteRefusedByTheSchemaOnce(t *testing.T) {
	t.Parallel()
	m := example(t)
	m.Decision = &manifest.Decision{SystemOne: &manifest.SystemOne{
		Backend: manifest.SystemOneClef, Endpoint: "https://clef.example.com", Version: "1",
		Timeout: manifest.Duration(time.Second), Model: core.ModelRoute{Provider: "anthropic"},
	}}
	got := manifest.Validate(m)
	if len(got) != 1 || got[0].Path != "decision.system_one.model.model" {
		t.Fatalf("want one violation at the missing model, got %v", got)
	}
}

package conformance

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Fake credentials, built at run time so that no credential-shaped literal
// sits in the source.
var (
	fakeGitHubToken = "ghp_" + strings.Repeat("0A", 18)
	fakeFineToken   = "github_pat_" + strings.Repeat("0a", 20)
	fakeAPIKey      = "sk-" + strings.Repeat("Ex4mple", 4)
	fakeAccessKeyID = "AKIA" + strings.Repeat("EXAMPLE0", 2)
)

// A token travels in more shapes than a password in a URL: in a URL's user
// part or query, as a flag of a command, in an Authorization header, or
// bare with the prefix its provider gives it.
func TestMoreShapesOfSecretsAreRefusedWhateverTheirKey(t *testing.T) {
	t.Parallel()
	refused := map[string]any{
		"token in a URL's user part":       "https://" + fakeGitHubToken + "@github.com/acme/app.git",
		"token in a URL's query":           "https://api.example.com/v1?access_token=opaque-example-value",
		"signature in a URL's query":       "https://bucket.example.com/o?X-Amz-Signature=0123456789abcdef&X-Amz-Expires=60",
		"key in a URL's query":             "https://maps.example.com/api?key=opaque-example-value&v=3",
		"flag that sets a token":           "--token=opaque-example-value",
		"flag that sets a password":        "-password=opaque-example-value",
		"Authorization header":             "Authorization: Basic b3BhcXVlOmV4YW1wbGU=",
		"bearer credential":                "Bearer opaque.example.value",
		"GitHub token":                     fakeGitHubToken,
		"fine-grained GitHub token":        fakeFineToken,
		"provider API key":                 fakeAPIKey,
		"access key ID":                    fakeAccessKeyID,
		"token inside a longer text":       "clone with " + fakeGitHubToken + " then build",
		"proxy Authorization header lower": "proxy-authorization: Bearer opaque-example-value",
	}
	for name, value := range refused {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if secretShape(value.(string)) == "" {
				t.Fatalf("%q has the shape of a secret", value)
			}
			report := Check(inCoderConfig(t, map[string]any{"note": value}))
			wantOnly(t, report, RuleSecretInline, "adapters.coder.config.note")
		})
	}
}

// A flag named like a secret takes the next word as its value.
func TestAFlagNamedLikeASecretDoesNotTakeAnInlineValue(t *testing.T) {
	t.Parallel()
	refused := map[string][]any{
		"args[1]": {"--token", "opaque-example-value"},
		"args[4]": {"--verbose", "--name", "worker", "--api-key", "opaque-example-value"},
	}
	for at, args := range refused {
		wantOnly(t, Check(inCoderConfig(t, map[string]any{"args": args})), RuleSecretInline, "adapters.coder.config."+at)
	}
	allowed := []any{"--token", "env:GITHUB_TOKEN", "--password-stdin", "registry.example.com", "--token-file", "/run/secrets/t",
		"--api-key", "--verbose", "--github-token=local:providers/github"}
	if report := Check(inCoderConfig(t, map[string]any{"args": allowed})); !report.Conformant() {
		t.Fatalf("flags given references, files or nothing hold no secret, got %+v", report.Findings)
	}
}

// Keys that name a secret in other words than the usual ones.
func TestKeysThatNameATokenAreRecognized(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"github_pat", "pat", "bearer", "bearer_token", "Bearer"} {
		if !namesSecret(key) {
			t.Errorf("key %q names a secret", key)
		}
		wantOnly(t, Check(inCoderConfig(t, map[string]any{key: "opaque-example-value"})), RuleSecretInline, "adapters.coder.config."+key)
	}
}

// The typed fields that hold free text are searched by shape too: what a
// unit runs, where a zone's source and health check are, where artifacts
// are kept, and the images.
func TestTypedTextFieldsAreSearchedForSecrets(t *testing.T) {
	t.Parallel()
	credentialed := "https://ci:opaque-example-value@git.example.com/acme/app.git"
	cases := map[string]func(m *manifest.Manifest){
		"units.control.command[2]": func(m *manifest.Manifest) {
			editUnit(m, manifest.UnitControl, func(u *manifest.Unit) {
				u.Command = []string{"factoryd", "control", "--upstream=" + credentialed}
			})
		},
		"units.app.command[2]": func(m *manifest.Manifest) {
			editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Command = []string{"serve", "--token", "opaque-example-value"} })
		},
		"units.edge.command[1]": func(m *manifest.Manifest) {
			editUnit(m, manifest.UnitEdge, func(u *manifest.Unit) { u.Command = []string{"edge", "GITHUB_TOKEN=opaque-example-value"} })
		},
		"components.agent.source": func(m *manifest.Manifest) { m.Components.Agent.Source = credentialed },
		"components.application.health_check": func(m *manifest.Manifest) {
			m.Components.Application.HealthCheck = "https://app.example.com/healthz?token=opaque-example-value"
		},
		"storage.artifacts.location": func(m *manifest.Manifest) {
			m.Storage.Artifacts.Kind = manifest.ArtifactsObjectStore
			m.Storage.Artifacts.Location = "s3://" + fakeAccessKeyID + ":opaque-example-value@bucket/sisf"
		},
		"release.low_traffic.synthetic_suite": func(m *manifest.Manifest) {
			m.Release.LowTraffic.SyntheticSuite = "https://suite.example.com/run?api_key=opaque-example-value"
		},
	}
	for path, change := range cases {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			m := docker(t)
			change(m)
			report := Check(m)
			if !report.reportedAt(RuleSecretInline, path) {
				t.Fatalf("want %s at %s, got %+v", RuleSecretInline, path, report.Findings)
			}
			for _, finding := range report.Findings {
				if strings.Contains(finding.Message+finding.Fix, "opaque-example-value") {
					t.Errorf("a finding repeats the secret: %+v", finding)
				}
			}
		})
	}

	// What these fields usually hold passes.
	m := docker(t)
	m.Components.Agent.Source = "ssh://git@github.com/acme/factory.git"
	m.Components.Application.Source = "git@github.com:acme/hello-app.git"
	editUnit(m, manifest.UnitControl, func(u *manifest.Unit) {
		u.Command = []string{"factoryd", "control", "--upstream=http://127.0.0.1:7461/v1?verbose=1", "--token-file", "/run/secrets/t"}
	})
	if report := Check(m); !report.Conformant() {
		t.Fatalf("sources, commands and URLs without secrets must be accepted, got %+v", report.Findings)
	}
}

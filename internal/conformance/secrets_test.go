package conformance

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

func TestAFieldThatNamesASecretHoldsAReference(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		path   string
		change func(m *manifest.Manifest)
	}{
		"a connection string":            {"storage.state.dsn_ref", func(m *manifest.Manifest) { m.Storage.State.DSNRef = "postgres://sisf@db.internal:5432/sisf" }},
		"a password after a colon":       {"storage.state.dsn_ref", func(m *manifest.Manifest) { m.Storage.State.DSNRef = "postgres:hunter2" }},
		"credentials pasted into a zone": {"components.agent.credentials_ref", func(m *manifest.Manifest) { m.Components.Agent.CredentialsRef = "tok_EXAMPLE0000000000000000000000" }},
		"a bare name for credentials":    {"components.control.credentials_ref", func(m *manifest.Manifest) { m.Components.Control.CredentialsRef = "zone3-control" }},
		"a provider that does not exist": {"components.application.credentials_ref", func(m *manifest.Manifest) {
			m.Components.Application.CredentialsRef = "vault:kv/sisf/zone1"
		}},
		"a name that is not a variable's": {"storage.state.dsn_ref", func(m *manifest.Manifest) { m.Storage.State.DSNRef = "env:hunter2" }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := docker(t)
			tc.change(m)
			wantOnly(t, Check(m), RuleSchema, tc.path)
		})
	}
}

// Without a usable reference to the root public key the supervisor cannot
// check the signed manifest, so self-update is disabled as well.
func TestTheRootKeyIsReferencedAndNeverWrittenInTheFile(t *testing.T) {
	t.Parallel()
	for _, written := range []string{"MCowBQYDK2VwAyEAexampleexampleexampleexampleexample0000", "root-public-key", "vault:kv/sisf/root", "env:root"} {
		m := docker(t)
		m.Supervisor.RootKeyRef = written
		report := Check(m)
		wantOnly(t, report, RuleSupervisorSelfCheck, "supervisor.root_key_ref")
		if len(report.Findings) != 1 || report.SelfUpdate || strings.Contains(report.Findings[0].Message, written) {
			t.Errorf("want one finding that does not repeat the value, and self-update disabled, got %+v", report)
		}
	}
}

func TestAFreeFormSettingNamedLikeASecretHoldsAReference(t *testing.T) {
	t.Parallel()
	const literal = "inline-example-value"
	refused := map[string]map[string]any{
		"api_key":                   {"api_key": literal},
		"apiKey":                    {"apiKey": literal},
		"API_KEY":                   {"API_KEY": literal},
		"token":                     {"token": literal},
		"github-token":              {"github-token": literal},
		"password":                  {"password": literal},
		"client_secret":             {"client_secret": literal},
		"private_key":               {"private_key": literal},
		"dsn":                       {"dsn": literal},
		"api_key_ref":               {"api_key_ref": literal}, // named a reference, yet holds none
		"api_keys[1]":               {"api_keys": []any{"env:FIRST_KEY", literal}},
		"auth.token":                {"auth": map[string]any{"token": literal}},
		"credentials.user":          {"credentials": map[string]any{"user": literal}}, // everything below a secret is one
		"providers[0].access_token": {"providers": []any{map[string]any{"name": "example", "access_token": literal}}},
	}
	for key, config := range refused {
		t.Run(key, func(t *testing.T) {
			for _, adapter := range []string{"coder", "qa", "execution", "deploy"} {
				m := docker(t)
				ref := map[string]*manifest.AdapterRef{"coder": &m.Adapters.Coder, "qa": &m.Adapters.QA,
					"execution": &m.Adapters.Execution, "deploy": &m.Adapters.Deploy}[adapter]
				ref.Config = config
				report := Check(m)
				wantOnly(t, report, RuleSecretInline, "adapters."+adapter+".config."+key)
				if len(report.Findings) != 1 {
					t.Errorf("one inline secret is one finding, got %+v", report.Findings)
				}
			}
			// Extensions are free-form in the same way.
			m := docker(t)
			m.Extensions = map[string]any{"x-team": config}
			wantOnly(t, Check(m), RuleSecretInline, "extensions.x-team."+key)
		})
	}

	// A mapping the schema refuses for its keys is still searched for secrets.
	m := docker(t)
	m.Adapters.Deploy.Config = map[string]any{"endpoint": map[any]any{"secret": literal, 1: "port"}}
	if report := Check(m); !report.Has(RuleSecretInline) || !report.Has(RuleSchema) {
		t.Fatalf("want the inline secret reported beside the malformed mapping, got %+v", report.Findings)
	}

	allowed := map[string]any{
		"api_key":     "env:ANTHROPIC_API_KEY",
		"api_key_ref": "local:providers/anthropic-api-key",
		"auth":        map[string]any{"token": "env:GITHUB_TOKEN", "scheme": "bearer"},
		"max_tokens":  4096, // a count, not a secret
		"model":       "mid",
		"cache_key":   "build-1",
		"sort_key":    "created_at",
		"tokenizer":   "bpe",
		"timeout":     "30s",
		"keys":        []any{"a", "b"},
		"streaming":   true,
	}
	m = docker(t)
	m.Adapters.Coder.Config = allowed
	m.Extensions = map[string]any{"x-team": allowed}
	if report := Check(m); !report.Conformant() {
		t.Fatalf("references and ordinary settings must be accepted, got %+v", report.Findings)
	}
}

// What the checker refuses may be a secret someone pasted into the file. The
// report says where it is and what belongs there, and never repeats it.
func TestNoFindingRepeatsAValueThatMayBeASecret(t *testing.T) {
	t.Parallel()
	const secret = "hunter2"
	cases := map[string]func(m *manifest.Manifest){
		"a connection string with a password": func(m *manifest.Manifest) {
			m.Storage.State.DSNRef = "postgres://sisf:" + secret + "@db.internal:5432/sisf"
		},
		"a password after a colon": func(m *manifest.Manifest) { m.Storage.State.DSNRef = "postgres:" + secret },
		"a token as credentials":   func(m *manifest.Manifest) { m.Components.Agent.CredentialsRef = "tok_" + secret + "000000000000000000" },
		"key material as the root key": func(m *manifest.Manifest) {
			m.Supervisor.RootKeyRef = "key-" + secret + " with spaces"
		},
		"one credential set for two zones": func(m *manifest.Manifest) {
			m.Components.Agent.CredentialsRef = "local:" + secret
			m.Components.Control.CredentialsRef = "local:" + secret
		},
		"an api key in adapter config": func(m *manifest.Manifest) {
			m.Adapters.Coder.Config = map[string]any{"api_key": secret, "auth": map[string]any{"password": secret}}
		},
		"a secret in an extension": func(m *manifest.Manifest) {
			m.Extensions = map[string]any{"x-team": map[string]any{"webhook_secret": secret}}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			m := docker(t)
			change(m)
			report := Check(m)
			if report.Conformant() {
				t.Fatal("the manifest was accepted")
			}
			for _, finding := range report.Findings {
				if finding.Path == "" || finding.Fix == "" {
					t.Errorf("finding %+v must say where and how to fix", finding)
				}
				for _, text := range []string{finding.Message, finding.Fix, finding.Path} {
					if strings.Contains(text, secret) {
						t.Errorf("finding [%s] at %s repeats the value: %q", finding.Rule, finding.Path, text)
					}
				}
			}
			if failure := report.Err(); failure == nil || strings.Contains(failure.Error(), secret) {
				t.Errorf("the error repeats the value: %v", failure)
			}
		})
	}
}

func TestAKeyNamesASecretByItsWords(t *testing.T) {
	t.Parallel()
	secret := []string{"api_key", "apiKey", "APIKey", "API_KEY", "apikey", "x-api-key", "private_key", "signing-key", "sshKey",
		"token", "bot_token", "accessToken", "tokens", "password", "db.password", "Passphrase", "client_secret", "secrets",
		"credentials", "dsn", "Authorization", "api_key_ref", "tokenRef"}
	for _, key := range secret {
		if !namesSecret(key) {
			t.Errorf("key %q names a secret", key)
		}
	}
	plain := []string{"", "ref", "model", "max_tokens_per_run", "token_budget", "tokenizer", "cache_key", "sort_key", "key", "keys",
		"primary_key", "keyboard", "monkey", "endpoint", "timeout", "author", "passthrough"}
	for _, key := range plain {
		if namesSecret(key) {
			t.Errorf("key %q does not name a secret", key)
		}
	}
}

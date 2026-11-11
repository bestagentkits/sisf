package conformance

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// inCoderConfig returns the docker example with one free-form setting.
func inCoderConfig(t *testing.T, config map[string]any) *manifest.Manifest {
	t.Helper()
	m := docker(t)
	m.Adapters.Coder.Config = config
	return m
}

// A secret is recognized by its own shape under any key, so that calling a
// setting something harmless does not let a secret into the file.
func TestAValueShapedLikeASecretIsRefusedWhateverItsKey(t *testing.T) {
	t.Parallel()
	const secret = "hunter2"
	refused := map[string]map[string]any{
		"signer":           {"signer": "-----BEGIN PRIVATE KEY-----\n" + secret + "\n-----END PRIVATE KEY-----"},
		"cert_bundle":      {"cert_bundle": "-----BEGIN RSA PRIVATE KEY-----\n" + secret},
		"identity":         {"identity": "-----BEGIN OPENSSH PRIVATE KEY-----\n" + secret},
		"pgp":              {"pgp": "-----BEGIN PGP PRIVATE KEY BLOCK-----\n" + secret},
		"sealed":           {"sealed": "-----BEGIN ENCRYPTED PRIVATE KEY-----\n" + secret},
		"database_url":     {"database_url": "postgres://sisf:" + secret + "@db.internal/sisf"},
		"cache":            {"cache": "redis://:" + secret + "@cache.internal:6379/0"},
		"upstream.url":     {"upstream": map[string]any{"url": "https://ci:" + secret + "@git.internal/acme/app.git"}},
		"args[1]":          {"args": []any{"--store", "mysql://root:" + secret + "@127.0.0.1/sisf"}},
		"env[1]":           {"env": []any{"LOG_LEVEL=debug", "API_KEY=" + secret}},
		"env[0]":           {"env": []any{"GITHUB_TOKEN=" + secret}},
		"environment[0]":   {"environment": []any{"DB_PASSWORD=" + secret}},
		"container.env[0]": {"container": map[string]any{"env": []any{"DATABASE_URL=postgres://u:" + secret + "@h/db"}}},
		"env.API_KEY":      {"env": map[string]any{"API_KEY": secret}},
	}
	for key, config := range refused {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			path := "adapters.coder.config." + key
			report := Check(inCoderConfig(t, config))
			wantOnly(t, report, RuleSecretInline, path)
			if len(report.Findings) != 1 {
				t.Fatalf("one inline secret is one finding, got %+v", report.Findings)
			}
			if finding := report.Findings[0]; strings.Contains(finding.Message+finding.Fix, secret) || strings.Contains(report.Err().Error(), secret) {
				t.Errorf("the finding repeats the value: %+v", finding)
			}
			// Extensions are searched the same way.
			m := docker(t)
			m.Extensions = map[string]any{"x-team": config}
			wantOnly(t, Check(m), RuleSecretInline, "extensions.x-team."+key)
		})
	}
}

func TestMoreNamesOfSecretsAreRecognized(t *testing.T) {
	t.Parallel()
	const literal = "inline-example-value"
	for _, key := range []string{"private_key_pem", "privateKeyPEM", "signing_key_b64", "api_key_value", "connection_string",
		"connectionString", "conn_str", "apikeys", "apikey", "pwd", "db_pwd", "jwt", "service_jwt", "secretkey", "accesskey"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			if !namesSecret(key) {
				t.Fatalf("key %q names a secret", key)
			}
			var value any = literal
			if key == "apikeys" {
				value = []any{literal}
				key += "[0]"
			}
			name, _, _ := strings.Cut(key, "[")
			wantOnly(t, Check(inCoderConfig(t, map[string]any{name: value})), RuleSecretInline, "adapters.coder.config."+key)
		})
	}
}

// A key that counts model tokens, or that says where a secret is kept, does
// not hold one; neither does text that only resembles a secret's shape.
func TestSettingsThatAreNotSecretsAreAccepted(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"max_tokens", "maxTokens", "max_output_tokens", "num_tokens", "context_tokens", "thinking_budget_tokens",
		"tls_secret_name", "secretName", "credentials_file", "token_path", "password_file", "secrets_dir", "jwt_issuer", "jwt_audience"} {
		if namesSecret(key) {
			t.Errorf("key %q does not name a secret", key)
		}
	}
	allowed := map[string]any{
		"max_tokens":        "4096", // a count written as text
		"max_output_tokens": "1024",
		"tls_secret_name":   "edge-tls",
		"credentials_file":  "/run/secrets/x",
		"token_path":        "/var/run/secrets/token",
		"ca_bundle":         "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----", // public
		"verify_key":        "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----",
		"endpoint":          "https://api.example.com:8443/v1",
		"registry":          "postgres://db.internal:5432/sisf", // no password in it
		"git_remote":        "ssh://git@git.internal/acme/app.git",
		"note":              "see https://example.com/a:b@c for the format",
		"env":               []any{"LOG_LEVEL=debug", "MAX_TOKENS=4096", "API_KEY=env:ANTHROPIC_API_KEY", "TOKEN_PATH=/run/secrets/t", "NO_PROXY"},
		"environment":       map[string]any{"API_KEY": "local:providers/anthropic", "REGION": "eu"},
		"api_key_env":       "ANTHROPIC_API_KEY", // the name of the variable, outside any env list
		"args":              []any{"--name=worker", "--token-file=/run/secrets/t"},
	}
	m := inCoderConfig(t, allowed)
	m.Extensions = map[string]any{"x-team": allowed}
	if report := Check(m); !report.Conformant() {
		t.Fatalf("settings that hold no secret must be accepted, got %+v", report.Findings)
	}
}

// A locator key is not judged by its name, yet what it holds is still
// judged by its shape.
func TestAKeyThatLocatesASecretIsStillJudgedByTheShapeOfItsValue(t *testing.T) {
	t.Parallel()
	config := map[string]any{"credentials_file": "-----BEGIN PRIVATE KEY-----\nEXAMPLEONLY"}
	wantOnly(t, Check(inCoderConfig(t, config)), RuleSecretInline, "adapters.coder.config.credentials_file")
}

// A key that says how a secret is found, checked or used describes it and
// holds none: an endpoint, a helper, a provider, a mode, a policy, a length,
// or the name of the variable that holds it. A switch such as
// use_credentials holds a yes or a no.
func TestSettingsThatDescribeASecretAreAccepted(t *testing.T) {
	t.Parallel()
	allowed := map[string]any{
		"authorization_endpoint": "https://idp.example.com/oauth2/authorize",
		"token_url":              "https://idp.example.com/oauth2/token",
		"jwks_uri":               "https://idp.example.com/.well-known/jwks.json",
		"credential_helper":      "osxkeychain",
		"secret_provider":        "vault",
		"credentials_mode":       "include",
		"use_credentials":        "true",
		"require_password":       "yes",
		"password_policy":        "strong",
		"password_min_length":    "12",
		"password_env":           "DB_PASSWORD",
		"dsn_env":                "SISF_STATE_DSN",
	}
	for key, value := range allowed {
		if report := Check(inCoderConfig(t, map[string]any{key: value})); !report.Conformant() {
			t.Errorf("%s describes a secret and holds none, got %+v", key, report.Findings)
		}
	}

	// A switch is a switch wherever its name stands: as a variable, a flag
	// or a query parameter.
	switches := map[string]any{
		"env":  []any{"ENABLE_PASSWORD=true", "USE_CREDENTIALS=0"},
		"args": []any{"--require-password", "yes", "--use-credentials=false"},
		"url":  "https://idp.example.com/x?use_credentials=true",
	}
	if report := Check(inCoderConfig(t, switches)); !report.Conformant() {
		t.Errorf("switches that hold a yes or a no hold no secret, got %+v", report.Findings)
	}
	switchHeld := map[string]map[string]any{
		"env[0]":  {"env": []any{"ENABLE_PASSWORD=opaque-example-value"}},
		"args[1]": {"args": []any{"--require-password", "opaque-example-value"}},
		"args[0]": {"args": []any{"--use-credentials=opaque-example-value"}},
		"url":     {"url": "https://idp.example.com/x?enable_password=opaque-example-value"},
	}
	for at, config := range switchHeld {
		wantOnly(t, Check(inCoderConfig(t, config)), RuleSecretInline, "adapters.coder.config."+at)
	}

	// Its value is still judged by its shape, and a switch holds only a
	// yes or a no.
	refused := map[string]any{
		"authorization_endpoint": "https://idp.example.com/authorize?client_secret=opaque-example-value",
		"credential_helper":      "--password=opaque-example-value",
		"use_credentials":        "opaque-example-value",
		"enable_api_key":         "opaque-example-value",
	}
	for key, value := range refused {
		wantOnly(t, Check(inCoderConfig(t, map[string]any{key: value})), RuleSecretInline, "adapters.coder.config."+key)
	}
}

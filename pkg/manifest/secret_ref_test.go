package manifest_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

func TestASecretReferenceNamesASecretAndNeverHoldsOne(t *testing.T) {
	t.Parallel()
	references := []string{"env:SISF_STATE_DSN", "env:_X", "local:zone1-application", "local:providers/anthropic-api-key"}
	for _, ref := range references {
		if !manifest.IsSecretRef(ref) {
			t.Errorf("%q is a reference and must be accepted", ref)
		}
	}
	literals := []string{
		"",
		"zone1-application",                  // a bare name says nothing about where the secret is kept
		"postgres:hunter2",                   // user:password has a colon, yet is no reference
		"postgres://sisf:hunter2@db:5432/db", // a connection string
		"tok_EXAMPLE0000000000000000000000",  // a token pasted into the file
		"env:lowercase",                      // not the name of an environment variable
		"env:",
		"local:../escape",
		"local:a//b",
		"vault:kv/sisf", // a provider without an adapter is not yet a provider
		" env:SISF_STATE_DSN",
	}
	for _, literal := range literals {
		if manifest.IsSecretRef(literal) {
			t.Errorf("%q is not a reference and must be refused", literal)
		}
	}
}

func TestEveryFieldThatNamesASecretAcceptsOnlyAReference(t *testing.T) {
	t.Parallel()
	schema := manifest.Schema()
	fields := [][]string{
		{"$defs", "StateStore", "properties", "dsn_ref"},
		{"$defs", "Component", "properties", "credentials_ref"},
		{"$defs", "Supervisor", "properties", "root_key_ref"},
	}
	samples := []string{"env:SISF_ROOT_PUBLIC_KEY", "local:zone3/control", "", "postgres:hunter2", "plain-name", "tok_EXAMPLE0000000000000000000000"}
	for _, at := range fields {
		pattern := regexp.MustCompile(dig(t, schema, append(at, "pattern")...).(string))
		for _, sample := range samples {
			if pattern.MatchString(sample) != manifest.IsSecretRef(sample) {
				t.Errorf("%s: the schema and IsSecretRef disagree about %q", strings.Join(at, "/"), sample)
			}
		}
	}
}

func TestARefusedValueIsNeverRepeated(t *testing.T) {
	t.Parallel()
	const password = "hunter2"
	changes := map[string]struct {
		path   string
		change func(m *manifest.Manifest)
	}{
		"a connection string with a password": {"storage.state.dsn_ref", func(m *manifest.Manifest) {
			m.Storage.State.DSNRef = "postgres://sisf:" + password + "@db.internal:5432/sisf"
		}},
		"a password after a colon": {"storage.state.dsn_ref", func(m *manifest.Manifest) { m.Storage.State.DSNRef = "postgres:" + password }},
		"credentials written in the file": {"components.agent.credentials_ref", func(m *manifest.Manifest) {
			m.Components.Agent.CredentialsRef = "tok_" + password + "0000000000000000000000"
		}},
		"a key written in the file": {"supervisor.root_key_ref", func(m *manifest.Manifest) { m.Supervisor.RootKeyRef = "key " + password }},
	}
	for name, tc := range changes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := example(t)
			tc.change(m)
			located := false
			for _, v := range manifest.Validate(m) {
				if strings.Contains(v.String(), password) {
					t.Errorf("the violation repeats the refused value: %s", v)
				}
				located = located || v.Path == tc.path && strings.Contains(v.Message, "pattern")
			}
			if !located {
				t.Errorf("want a violation at %s that states the expected form", tc.path)
			}
		})
	}

	// The same holds for the error of a file that is loaded.
	bad := filepath.Join(t.TempDir(), "factory.yaml")
	text := strings.Replace(exampleYAML(t), "dsn_ref: env:SISF_STATE_DSN", "dsn_ref: postgres://sisf:"+password+"@db.internal:5432/sisf", 1)
	if err := os.WriteFile(bad, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := manifest.Load(bad)
	if err == nil || !strings.Contains(err.Error(), "storage.state.dsn_ref") {
		t.Fatalf("the file must be refused at storage.state.dsn_ref, got %v", err)
	}
	if strings.Contains(err.Error(), password) {
		t.Fatalf("the error repeats the refused value: %v", err)
	}
}

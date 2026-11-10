package systemone

import (
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

func validConfig() Config {
	return Config{Backend: BackendJev, Endpoint: "https://jev.example.test/v1/judge", Version: "2026-11-01",
		Timeout: 5 * time.Second, Credential: "env:SISF_JEV_TOKEN", Model: core.ModelRoute{Provider: "jev", Model: "jev-1"}}
}

func TestConfigAcceptsAPinnedEndpointWithASecretReference(t *testing.T) {
	t.Parallel()
	for _, backend := range []Backend{BackendJev, BackendClef} {
		cfg := validConfig()
		cfg.Backend = backend
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%s: %v", backend, err)
		}
	}
	cfg := validConfig()
	cfg.Endpoint, cfg.Credential = "http://127.0.0.1:7400/judge", ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("plain HTTP on loopback without a credential is allowed for local fakes: %v", err)
	}
	ref, ok, err := cfg.CredentialRef()
	if err != nil || ok || ref.Name != "" {
		t.Fatalf("no credential configured: %+v %v %v", ref, ok, err)
	}
}

func TestConfigRefusesInlineCredentialsAndUnpinnedEndpoints(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Config){
		"unknown backend":            func(c *Config) { c.Backend = "oracle" },
		"no endpoint":                func(c *Config) { c.Endpoint = "" },
		"relative endpoint":          func(c *Config) { c.Endpoint = "/judge" },
		"credentials in the URL":     func(c *Config) { c.Endpoint = "https://user:hunter2@jev.example.test/judge" },
		"token in the query":         func(c *Config) { c.Endpoint = "https://jev.example.test/judge?token=abc" },
		"plain HTTP to a remote":     func(c *Config) { c.Endpoint = "http://jev.example.test/judge" },
		"other scheme":               func(c *Config) { c.Endpoint = "ftp://jev.example.test/judge" },
		"no version":                 func(c *Config) { c.Version = "" },
		"floating version":           func(c *Config) { c.Version = "latest" },
		"version with spaces":        func(c *Config) { c.Version = "1 2" },
		"no timeout":                 func(c *Config) { c.Timeout = 0 },
		"timeout too long":           func(c *Config) { c.Timeout = 10 * time.Minute },
		"inline credential":          func(c *Config) { c.Credential = "sk-live-0123456789" },
		"no model route":             func(c *Config) { c.Model = core.ModelRoute{} },
		"credential with bad prefix": func(c *Config) { c.Credential = "vault:jev" },
	}
	for name, mutate := range cases {
		cfg := validConfig()
		mutate(&cfg)
		err := cfg.Validate()
		if !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "sk-live") {
			t.Errorf("%s: the error repeats a secret: %v", name, err)
		}
	}
}

func TestCredentialReferenceIsAControlPlaneSecret(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Credential = "local:system-one/jev"
	ref, ok, err := cfg.CredentialRef()
	if err != nil || !ok {
		t.Fatalf("CredentialRef: %v %v", ok, err)
	}
	if ref.Provider != "local" || ref.Name != "system-one/jev" || ref.Class != "control" || ref.Zone != core.ZoneControl {
		t.Fatalf("ref = %+v", ref)
	}
}

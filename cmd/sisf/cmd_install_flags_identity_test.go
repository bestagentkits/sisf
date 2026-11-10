package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/install"
)

// parsedInstallFlags parses init's flags.
func parsedInstallFlags(t *testing.T, args ...string) installFlags {
	t.Helper()
	var f installFlags
	fs := newFlagSet("init")
	bindInstallFlags(fs, &f)
	if err := parseFlags(fs, args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return f
}

func TestSignInFlagsReadClientSecretsFromAFileOrAVariableOnly(t *testing.T) {
	t.Parallel()
	secretFile := filepath.Join(t.TempDir(), "oidc-secret")
	if err := os.WriteFile(secretFile, []byte("oidc-secret-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"GH_TOKEN": "gh-token", "GH_LOGIN_SECRET": "gh-login-secret"}
	getenv := func(k string) string { return env[k] }
	f := parsedInstallFlags(t, "--repo", "acme/app", "--github-token-env", "GH_TOKEN",
		"--external-url", "https://factory.example.com", "--owner", "https://accounts.example.com#1", "--owner", "https://github.com#2",
		"--oidc-issuer", "https://accounts.example.com", "--oidc-client-id", "sisf", "--oidc-client-secret-file", secretFile,
		"--github-login-client-id", "gh-app", "--github-login-client-secret-env", "GH_LOGIN_SECRET")
	o, err := f.options("/tmp/factory", getenv)
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	id := o.Identity
	if id.ExternalURL != "https://factory.example.com" || len(id.Owners) != 2 || id.OIDCIssuer != "https://accounts.example.com" ||
		id.OIDCClientID != "sisf" || id.GitHubClientID != "gh-app" {
		t.Fatalf("identity = %+v", id)
	}
	if string(id.OIDCClientSecret.Reveal()) != "oidc-secret-from-file" || string(id.GitHubClientSecret.Reveal()) != "gh-login-secret" {
		t.Fatal("the client secrets were not read from the file and the variable")
	}
	if strings.Contains(fmt.Sprintf("%v %+v", o, id), "oidc-secret-from-file") {
		t.Fatal("the options format a client secret as its value")
	}
	for name, args := range map[string][]string{
		"empty variable":   {"--oidc-client-secret-env", "UNSET"},
		"unreadable file":  {"--oidc-client-secret-file", filepath.Join(t.TempDir(), "missing")},
		"bad variable":     {"--github-login-client-secret-env", "A B"},
		"two ways at once": {"--github-login-client-secret-env", "GH_LOGIN_SECRET", "--github-login-client-secret-file", secretFile},
	} {
		f := parsedInstallFlags(t, append([]string{"--repo", "acme/app", "--github-token-env", "GH_TOKEN"}, args...)...)
		_, err := f.options("/tmp/factory", getenv)
		var usage *usageError
		if !errors.As(err, &usage) || strings.Contains(err.Error(), "gh-login-secret") {
			t.Errorf("%s: options = %v, want a usage error that shows no value", name, err)
		}
	}
}

func TestAProductionInitWithoutSignInIsRefusedWithWhatToPass(t *testing.T) {
	t.Parallel()
	getenv := func(k string) string { return map[string]string{"GH_TOKEN": "gh-token"}[k] }
	f := parsedInstallFlags(t, "--mode", "standalone", "--repo", "acme/app", "--github-token-env", "GH_TOKEN", "--confirm-unlimited", "--no-alert")
	o, err := f.options(filepath.Join(t.TempDir(), "factory"), getenv)
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	var stdout strings.Builder
	in := &install.Installer{Layout: install.Layout{Dir: o.Dir}, Out: &stdout}
	_, err = in.Init(t.Context(), o)
	if err == nil || !strings.Contains(err.Error(), "--oidc-issuer") || !strings.Contains(err.Error(), "--github-login-client-id") {
		t.Fatalf("Init = %v, want a refusal naming the sign-in flags", err)
	}
	if _, statErr := os.Stat(o.Dir); statErr == nil {
		t.Error("a refused init left a directory behind")
	}
}

func TestTheDevAndBudgetFlagsReachTheOptions(t *testing.T) {
	t.Parallel()
	f := parsedInstallFlags(t, "--mode", "standalone", "--repo", "bestagentkits/sisf-sandbox", "--dev", "--github-credential", "gh-cli",
		"--budget-cap", "20", "--alert-threshold", "15", "--run-estimate", "4", "--coder-agent", "claude", "--coder-model", "claude-sonnet-4-5",
		"--qa-judge", "agent", "--qa-agent", "codex", "--qa-model", "gpt-5-codex", "--agent-home", "/Users/op", "--qa-home", "/Users/op",
		"--exec", "local", "--build-command", "go build ./...")
	o, err := f.options("/srv/sisf/factory", func(string) string { return "" })
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	if !o.Dev || o.GitHubCredential != install.GitHubCredentialGHCLI || o.QAAgent != "codex" || o.BuildCommand != "go build ./..." {
		t.Fatalf("options = %+v", o)
	}
	a := o.Agents
	if a.Coder != "claude" || a.CoderModel != "claude-sonnet-4-5" || a.QAModel != "gpt-5-codex" || a.AgentHome != "/Users/op" || a.QAHome != "/Users/op" {
		t.Fatalf("agents = %+v", a)
	}
	b, given, err := f.budget()
	if err != nil || !given || b.CapMicros != 20_000_000 || b.AlertThresholdMicros != 15_000_000 || b.RunEstimateMicros != 4_000_000 {
		t.Fatalf("budget = %+v (%v, %v)", b, given, err)
	}
}

func TestTheBuildSandboxFlagsReachTheOptions(t *testing.T) {
	t.Parallel()
	image := "127.0.0.1:17481/sisf-runner@sha256:" + strings.Repeat("cd", 32)
	f := parsedInstallFlags(t, "--mode", "standalone", "--repo", "bestagentkits/sisf-sandbox", "--dev", "--github-credential", "gh-cli",
		"--coder-agent", "claude", "--qa-judge", "agent", "--qa-agent", "codex", "--agent-home", "/Users/op",
		"--exec", "docker", "--exec-docker-endpoint", "http://127.0.0.1:17480", "--exec-docker-network", "sisf-smoke-sandbox",
		"--egress-proxy", "http://egress:3128", "--runner-image", image, "--qa-agent-binary", "/srv/smoke/qa-launcher",
		"--build-command", "/usr/local/bin/sisf-build-candidate")
	o, err := f.options("/srv/sisf/factory", func(string) string { return "" })
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	want := install.BuildSandbox{Endpoint: "http://127.0.0.1:17480", Network: "sisf-smoke-sandbox", EgressProxy: "http://egress:3128", RunnerImage: image}
	if o.BuildSandbox != want || o.QAAgentBinary != "/srv/smoke/qa-launcher" || o.BuildCommand != "/usr/local/bin/sisf-build-candidate" {
		t.Fatalf("options = %+v", o)
	}
}

func TestTheBuildSandboxFlagsComeTogetherWithExecDocker(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"docker without a sandbox":      {"--mode", "standalone", "--exec", "docker"},
		"a sandbox without docker":      {"--mode", "standalone", "--exec-docker-network", "n"},
		"a sandbox with local":          {"--mode", "standalone", "--exec", "local", "--runner-image", "sha256:ab"},
		"a sandbox flag in docker mode": {"--mode", "docker", "--exec-docker-endpoint", "http://127.0.0.1:1"},
		"local in docker mode":          {"--mode", "docker", "--exec", "local"},
		"an unknown execution backend":  {"--mode", "standalone", "--exec", "podman"},
	} {
		f := parsedInstallFlags(t, append([]string{"--repo", "acme/app"}, args...)...)
		if _, err := f.options("/srv/sisf/factory", func(string) string { return "" }); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

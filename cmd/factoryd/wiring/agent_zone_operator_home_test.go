//go:build unix

package wiring_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// homeZone builds a claude agent zone whose agent runs with home.
func homeZone(t *testing.T, cfg wiring.AgentZoneConfig) (*wiring.AgentZone, error) {
	t.Helper()
	source, _ := gitSource(t)
	env := dispatcherEnv("/usr/bin")
	cfg.Store, cfg.Clock, cfg.Owner = nopStore{}, core.SystemClock{}, "exporter@test"
	if cfg.Agent == "" {
		cfg.Agent = "claude"
	}
	if cfg.WorkspaceRoot == "" {
		cfg.WorkspaceRoot = t.TempDir()
	}
	cfg.WorkspaceSource, cfg.ArtifactDir = source, t.TempDir()
	cfg.LookupEnv = func(n string) (string, bool) { v, ok := env[n]; return v, ok }
	return wiring.NewAgentZone(cfg)
}

func TestTheOperatorsHomeIsAcceptedOnlyUnderTrustedCodeAndApartFromTheFactorysDirectories(t *testing.T) {
	t.Parallel()
	install := t.TempDir()
	secrets, state := filepath.Join(install, "secrets"), filepath.Join(install, "data", "state")
	for _, dir := range []string{secrets, state} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	linkToSecrets := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(secrets, linkToSecrets); err != nil {
		t.Fatal(err)
	}
	protected := []string{install, state}
	trusted := &manifest.Manifest{TrustedCodeOnly: true}
	docker := &manifest.Manifest{TrustedCodeOnly: true, Adapters: manifest.Adapters{Execution: manifest.AdapterRef{Name: "docker"}}}
	operator := t.TempDir()
	for name, c := range map[string]struct {
		cfg  wiring.AgentZoneConfig
		want core.ErrorCode
	}{
		"under trusted code only": {wiring.AgentZoneConfig{Manifest: trusted, Home: operator, ProtectedDirs: protected}, ""},
		"codex under trusted code only": {wiring.AgentZoneConfig{Agent: "codex", Manifest: trusted, Home: operator,
			ProtectedDirs: protected}, ""},
		"without trusted code only": {wiring.AgentZoneConfig{Manifest: &manifest.Manifest{}, Binary: "/opt/sisf/launcher", Home: operator},
			core.CodePermissionDenied},
		"sandboxed":       {wiring.AgentZoneConfig{Manifest: docker, Sandboxed: true, Home: operator}, core.CodePermissionDenied},
		"with a launcher": {wiring.AgentZoneConfig{Manifest: trusted, Binary: "/opt/sisf/launcher", Home: operator}, core.CodePermissionDenied},
		"pi, which is always sandboxed": {wiring.AgentZoneConfig{Agent: "pi", Manifest: trusted, Sandboxed: true, Home: operator},
			core.CodePermissionDenied},
		"the scripted agent": {wiring.AgentZoneConfig{Agent: "fake", Home: operator}, core.CodeInvalidArgument},
		"the secrets":        {wiring.AgentZoneConfig{Manifest: trusted, Home: secrets, ProtectedDirs: protected}, core.CodePermissionDenied},
		"a link to the secrets": {wiring.AgentZoneConfig{Manifest: trusted, Home: linkToSecrets, ProtectedDirs: protected},
			core.CodePermissionDenied},
		"the state store's directory": {wiring.AgentZoneConfig{Manifest: trusted, Home: state, ProtectedDirs: protected}, core.CodePermissionDenied},
		"a home holding the install": {wiring.AgentZoneConfig{Manifest: trusted, Home: filepath.Dir(install), ProtectedDirs: protected},
			core.CodePermissionDenied},
		"a relative home": {wiring.AgentZoneConfig{Manifest: trusted, Home: "home/operator"}, core.CodeInvalidArgument},
		"a home that does not exist": {wiring.AgentZoneConfig{Manifest: trusted, Home: filepath.Join(operator, "missing")},
			core.CodeInvalidArgument},
	} {
		_, err := homeZone(t, c.cfg)
		if c.want == "" && err != nil || c.want != "" && !core.HasCode(err, c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// With --agent-home the agent finds the operator's home, where its login
// is kept, and its temporary files still go to its private directory.
func TestAnAgentWithTheOperatorsHomeKeepsAPrivateTemporaryDirectory(t *testing.T) {
	t.Parallel()
	record := t.TempDir()
	bin := clitest.Install(t, "claude", clitest.Script{Record: record})
	env := dispatcherEnv(bin)
	source, base := gitSource(t)
	root, operator := t.TempDir(), t.TempDir()
	zone, err := wiring.NewAgentZone(wiring.AgentZoneConfig{
		Store: nopStore{}, Clock: core.SystemClock{}, Agent: "claude", Owner: "exporter@test",
		WorkspaceRoot: root, WorkspaceSource: source, ArtifactDir: t.TempDir(),
		Manifest: &manifest.Manifest{TrustedCodeOnly: true}, Home: operator,
		LookupEnv: func(n string) (string, bool) { v, ok := env[n]; return v, ok },
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := zone.Agent.Start(context.Background(), codingTask("claude"), agent.WorkspaceRef{ID: "run-0001", BaseSHA: base}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		status, err := zone.Agent.Status(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		if status.Phase != agent.PhaseRunning && status.Phase != agent.PhaseQueued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the agent run did not end")
		}
	}
	seen := clitest.Recorded(t, record).Env
	for _, want := range []string{"HOME=" + operator, "TMPDIR=" + filepath.Join(root, ".agent-home", "tmp")} {
		if !slices.Contains(seen, want) {
			t.Errorf("the agent lacks %s", want)
		}
	}
}

func TestOnlyASQLiteStateStoreNamesALocalDirectory(t *testing.T) {
	t.Parallel()
	for dsn, want := range map[string]string{
		"/srv/sisf/data/state/factory.db":        "/srv/sisf/data/state",
		"postgres://agent:secret@db:5432/sisf":   "",
		"postgresql://agent:secret@db:5432/sisf": "",
		"":                                       "",
	} {
		if got := wiring.StateStoreDir(dsn); got != want {
			t.Errorf("a DSN of the %q kind names %q, want %q", filepath.Ext(dsn), got, want)
		}
	}
}

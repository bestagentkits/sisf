//go:build unix

package wiring_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state"
)

// dispatcherEnv is the dispatcher's own environment: its secrets, the
// variables of other tools, and the agents' provider credentials.
func dispatcherEnv(bin string) map[string]string {
	return map[string]string{
		"PATH": bin + ":/usr/bin:/bin", "HOME": "/home/dispatcher",
		"SISF_STATE_DSN": "postgres://agent:secret@db/sisf", "SISF_DISPATCHER_ADMIN_TOKEN": "run-control-token",
		"EDGE_ADMIN_TOKEN": "edge-token", "DOCKER_HOST": "unix:///var/run/docker.sock", "UNRELATED_PARENT_VAR": "parent",
		"ANTHROPIC_API_KEY": "sk-ant-agent", "CLAUDE_CODE_OAUTH_TOKEN": "oauth-agent", "OPENAI_API_KEY": "sk-oai-agent",
		"CODEX_API_KEY": "codex-agent", "PROXY_CA": "/etc/ca.pem",
	}
}

// gitSource makes a repository with one commit to check workspaces out of.
func gitSource(t *testing.T) (string, core.CommitSHA) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-q", "-m", "init")
	return dir, core.CommitSHA(run("rev-parse", "HEAD"))
}

// nopStore is a store the agent zone is built with but never reaches here.
type nopStore struct{}

func (nopStore) Update(context.Context, func(context.Context, state.Tx) error) error {
	return core.NewError(core.CodeUnavailable, "no store")
}

func (nopStore) View(context.Context, func(context.Context, state.Tx) error) error {
	return core.NewError(core.CodeUnavailable, "no store")
}

func (nopStore) Close() error { return nil }

func agentZone(t *testing.T, name string, m *manifest.Manifest, sandboxed bool, binary string, env map[string]string, extra ...string) (*wiring.AgentZone, error) {
	t.Helper()
	source, _ := gitSource(t)
	return wiring.NewAgentZone(wiring.AgentZoneConfig{
		Store: nopStore{}, Clock: core.SystemClock{}, Agent: name, Owner: "exporter@test",
		WorkspaceRoot: t.TempDir(), WorkspaceSource: source, ArtifactDir: t.TempDir(),
		Sandboxed: sandboxed, Binary: binary, Manifest: m, AgentEnv: extra,
		LookupEnv: func(n string) (string, bool) { v, ok := env[n]; return v, ok },
	})
}

func TestARealAgentNeedsASandboxALauncherOrTheTrustedCodeOnlyModel(t *testing.T) {
	t.Parallel()
	env := dispatcherEnv("/usr/bin")
	local := &manifest.Manifest{Adapters: manifest.Adapters{Execution: manifest.AdapterRef{Name: "local"}}}
	docker := &manifest.Manifest{Adapters: manifest.Adapters{Execution: manifest.AdapterRef{Name: "docker"}}}
	trusted := &manifest.Manifest{TrustedCodeOnly: true}
	for name, c := range map[string]struct {
		agent     string
		m         *manifest.Manifest
		sandboxed bool
		binary    string
		extra     []string
		want      core.ErrorCode
	}{
		"no sandbox, launcher or trust":       {"claude", local, false, "", nil, core.CodePermissionDenied},
		"sandboxed without isolating backend": {"codex", local, true, "", nil, core.CodePermissionDenied},
		"an isolating backend, not sandboxed": {"codex", docker, false, "", nil, core.CodePermissionDenied},
		"no manifest":                         {"claude", nil, true, "", nil, core.CodePermissionDenied},
		"a relative launcher":                 {"claude", local, false, "bin/launcher", nil, core.CodeInvalidArgument},
		"pi without a sandbox":                {"pi", trusted, false, "", nil, core.CodePermissionDenied},
		"pi with a launcher only":             {"pi", local, false, "/opt/sisf/launcher", nil, core.CodePermissionDenied},
		"the state store's credentials":       {"claude", trusted, false, "", []string{"SISF_STATE_DSN"}, core.CodePermissionDenied},
		"the edge's token":                    {"claude", trusted, false, "", []string{"EDGE_ADMIN_TOKEN"}, core.CodePermissionDenied},
		"the Docker endpoint":                 {"codex", trusted, false, "", []string{"DOCKER_HOST"}, core.CodePermissionDenied},
		"sandboxed with an isolating backend": {"codex", docker, true, "", nil, ""},
		"a sandboxing launcher":               {"claude", local, false, "/opt/sisf/launcher", nil, ""},
		"trusted code only":                   {"claude", trusted, false, "", []string{"PROXY_CA"}, ""},
		"pi sandboxed under trusted code":     {"pi", trusted, true, "", nil, ""},
	} {
		_, err := agentZone(t, c.agent, c.m, c.sandboxed, c.binary, env, c.extra...)
		if c.want == "" && err != nil || c.want != "" && !core.HasCode(err, c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	// The scripted agent runs no program and needs none of it.
	if _, err := agentZone(t, "fake", nil, false, "", env); err != nil {
		t.Errorf("the scripted agent: %v", err)
	}
}

// Each real agent's process sees only its allow-list: never the
// dispatcher's state store credentials, run control token or any other of
// its variables, and only its own provider's credentials.
func TestARealAgentSeesNoneOfTheDispatchersVariables(t *testing.T) {
	// Not parallel: the dispatcher's secrets are also put in this process's
	// own environment, which an agent must not inherit either.
	for name, value := range dispatcherEnv("") {
		if name != "PATH" {
			t.Setenv(name, value)
		}
	}
	for agentName, want := range map[string][]string{
		"claude": {"ANTHROPIC_API_KEY=sk-ant-agent", "CLAUDE_CODE_OAUTH_TOKEN=oauth-agent", "PROXY_CA=/etc/ca.pem"},
		"codex":  {"OPENAI_API_KEY=sk-oai-agent", "CODEX_API_KEY=codex-agent", "PROXY_CA=/etc/ca.pem"},
		"pi":     {"PROXY_CA=/etc/ca.pem"},
	} {
		t.Run(agentName, func(t *testing.T) {
			record := t.TempDir()
			bin := clitest.Install(t, agentName, clitest.Script{Record: record})
			env := dispatcherEnv(bin)
			source, base := gitSource(t)
			root := t.TempDir()
			zone, err := wiring.NewAgentZone(wiring.AgentZoneConfig{
				Store: nopStore{}, Clock: core.SystemClock{}, Agent: agentName, Owner: "exporter@test",
				WorkspaceRoot: root, WorkspaceSource: source, ArtifactDir: t.TempDir(),
				Sandboxed: true, Manifest: &manifest.Manifest{TrustedCodeOnly: true}, AgentEnv: []string{"PROXY_CA"},
				LookupEnv: func(n string) (string, bool) { v, ok := env[n]; return v, ok },
			})
			if err != nil {
				t.Fatal(err)
			}
			ref, err := zone.Agent.Start(context.Background(), codingTask(agentName), agent.WorkspaceRef{ID: "run-0001", BaseSHA: base}, nil)
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
			seen := map[string]bool{}
			for _, kv := range clitest.Recorded(t, record).Env {
				seen[kv] = true
				name, value, _ := strings.Cut(kv, "=")
				if parent, set := env[name]; set && parent == value && name != "PATH" && !strings.Contains(strings.Join(want, " "), kv) {
					t.Errorf("the agent inherited the dispatcher's %s", name)
				}
				for _, secret := range []string{"secret", "run-control-token", "edge-token", "docker.sock", "/home/dispatcher"} {
					if strings.Contains(value, secret) {
						t.Errorf("the agent's %s holds a value of the dispatcher's", name)
					}
				}
			}
			home := filepath.Join(root, ".agent-home")
			for _, kv := range append(want, "PATH="+env["PATH"], "HOME="+home, "TMPDIR="+filepath.Join(home, "tmp")) {
				if !seen[kv] {
					t.Errorf("the agent lacks %s", strings.SplitN(kv, "=", 2)[0])
				}
			}
		})
	}
}

func codingTask(agentName string) agent.Task {
	sha := core.CommitSHA("0123456789abcdef0123456789abcdef01234567")
	route := core.ModelRoute{Provider: "anthropic", Model: "mid"}
	if agentName == "codex" {
		route.Provider = "openai"
	}
	return agent.Task{
		RunID: "run-0001", IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 7}, Role: agent.RoleCoder, Step: core.StepCoding,
		Handoff: agent.Handoff{SoW: "Greet the world.", DoD: "It greets the world."},
		Policy: core.PolicyRefs{PolicySHA: sha, ReviewSHA: sha, DecisionSHA: sha, FeatureCatalogSHA: sha,
			ModelRoute: route},
		Limits: agent.Limits{Timeout: time.Minute}, IdempotencyKey: "run-0001/attempt/1",
	}
}

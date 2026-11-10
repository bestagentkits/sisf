//go:build unix

package main

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	execlocal "github.com/bestagentkits/sisf/adapters/exec/local"
	"github.com/bestagentkits/sisf/internal/qa/agentjudge"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// isolatingExec stands in for the docker backend: it proves isolation and
// egress enforcement, or not, and runs nothing.
type isolatingExec struct {
	execution.Backend
	proven bool
}

func (b isolatingExec) Capabilities(context.Context) (core.CapabilitySet, error) {
	if !b.proven {
		return core.CapabilitySet{core.CapExecIsolation: core.SupportNotSupported}, nil
	}
	return core.CapabilitySet{core.CapExecIsolation: core.SupportSupported, core.CapExecEgressEnforcement: core.SupportSupported}, nil
}

func TestTheQAAgentRunsOnlyUnderTheUntrustedCodeRule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	proven, unproven := isolatingExec{proven: true}, isolatingExec{}
	type choice struct {
		agent, exec string
		docker      execution.Backend
		trusted     bool
		launcher    string
	}
	allowed := map[string]choice{
		"codex under docker with a launcher":  {"codex", "docker", proven, false, "/usr/local/bin/codex-sandboxed"},
		"claude under docker with a launcher": {"claude", "docker", proven, false, "/usr/local/bin/claude-sandboxed"},
		"pi under docker with a launcher":     {"pi", "docker", proven, false, "/usr/local/bin/pi-sandboxed"},
		"claude locally under trusted code":   {"claude", "local", nil, true, ""},
		"codex locally under trusted code":    {"codex", "local", nil, true, ""},
	}
	for name, c := range allowed {
		if err := qaSandboxAllowed(ctx, c.agent, c.exec, c.docker, c.trusted, c.launcher); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	refused := map[string]struct {
		choice
		want core.ErrorCode
	}{
		"no backend":                       {choice{"codex", "none", nil, true, ""}, core.CodeNotSupported},
		"docker that proves no isolation":  {choice{"codex", "docker", unproven, false, ""}, core.CodePermissionDenied},
		"local without trusted code":       {choice{"codex", "local", nil, false, ""}, core.CodePermissionDenied},
		"pi locally even for trusted code": {choice{"pi", "local", nil, true, "/usr/local/bin/pi-sandboxed"}, core.CodePermissionDenied},
		"pi without a launcher":            {choice{"pi", "docker", proven, false, ""}, core.CodePermissionDenied},
		// Without a launcher the agent is the supervisor's own OS user,
		// which can read the trust directory.
		"codex under docker without a launcher":            {choice{"codex", "docker", proven, false, ""}, core.CodePermissionDenied},
		"claude under docker without a launcher":           {choice{"claude", "docker", proven, false, ""}, core.CodePermissionDenied},
		"a relative launcher":                              {choice{"codex", "docker", proven, false, "bin/codex"}, core.CodeInvalidArgument},
		"docker that proves no isolation, with a launcher": {choice{"codex", "docker", unproven, false, "/usr/local/bin/codex-sandboxed"}, core.CodePermissionDenied},
		"an unknown agent":                                 {choice{"fake", "docker", proven, false, ""}, core.CodeInvalidArgument},
	}
	for name, c := range refused {
		if err := qaSandboxAllowed(ctx, c.agent, c.exec, c.docker, c.trusted, c.launcher); !core.HasCode(err, c.want) {
			t.Errorf("%s: %v, want %s", name, err, c.want)
		}
	}
}

// judgeSetup returns flags and dependencies of a production factory whose
// QA agent is a Codex stand-in.
func judgeSetup(t *testing.T, env map[string]string) (*lifecycleFlags, judgeDeps) {
	t.Helper()
	bin := clitest.Install(t, "codex", clitest.Script{})
	l := &lifecycleFlags{coderAgent: "claude", exec: "docker", rootPrivateKeyEnv: "ROOT_KEY",
		qa: qaFlags{judge: qaJudgeAgent, agent: "codex", binary: filepath.Join(bin, "codex"), workspaceRoot: filepath.Join(t.TempDir(), "qa"),
			timeout: agentjudge.DefaultTimeout}}
	lookup := func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
	return l, judgeDeps{
		manifest: &manifest.Manifest{FactoryID: "acme-hello"}, docker: isolatingExec{proven: true}, trustDir: t.TempDir(),
		dsnEnv: "STATE_DSN", rootKeyEnv: "ROOT_KEY", lookupEnv: lookup, clock: core.SystemClock{},
		materials: agentjudge.MaterialsFunc(func(context.Context, qa.Candidate) (agentjudge.Materials, error) {
			return agentjudge.Materials{}, nil
		}),
	}
}

func TestAProductionFactoryWithARealCoderGetsTheAgentJudge(t *testing.T) {
	t.Parallel()
	l, d := judgeSetup(t, map[string]string{"PATH": "/usr/bin:/bin"})
	judge, err := l.judge(context.Background(), d)
	if err != nil {
		t.Fatalf("the agent judge was refused: %v", err)
	}
	if _, ok := judge.(*agentjudge.Judge); !ok {
		t.Fatalf("judge is %T", judge)
	}

	// The scripted judge never stands in outside the trusted simulation.
	l.qa.judge = qaJudgeFake
	if _, err := l.judge(context.Background(), d); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("the scripted judge in production: %v", err)
	}
	d.sim = true
	if _, err := l.judge(context.Background(), d); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("the scripted judge with a real coder: %v", err)
	}
	l.coderAgent = "fake"
	if _, err := l.judge(context.Background(), d); err != nil {
		t.Fatalf("the trusted simulation: %v", err)
	}

	// Under docker the agent on PATH, run as the supervisor's own user,
	// could read the trust directory: no launcher, no start.
	l, d = judgeSetup(t, map[string]string{"PATH": filepath.Dir(l.qa.binary)})
	l.qa.binary = ""
	if _, err := l.judge(context.Background(), d); !core.HasCode(err, core.CodePermissionDenied) || !strings.Contains(err.Error(), "trust directory") {
		t.Fatalf("an agent judge under docker without a launcher: %v", err)
	}

	// A QA agent that is not installed refuses the start.
	l, d = judgeSetup(t, nil)
	l.qa.binary = filepath.Join(t.TempDir(), "codex")
	if _, err := l.judge(context.Background(), d); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("a missing QA agent: %v", err)
	}
}

func TestTheQAAgentSeesItsOwnCredentialsAndNoneOfTheSupervisors(t *testing.T) {
	t.Parallel()
	secrets := map[string]string{
		"PATH": "/opt/bin:/usr/bin", "OPENAI_API_KEY": "sk-qa-key", "ANTHROPIC_API_KEY": "sk-coder-key",
		"SISF_GITHUB_TOKEN": "ghs-token", "EDGE_ADMIN_TOKEN": "edge-token", "ROOT_KEY": "root-key", "STATE_DSN": "postgres://u:p@db",
		"PROXY_CA": "/etc/ca.pem",
	}
	l, d := judgeSetup(t, secrets)
	l.qa.env = "PROXY_CA"
	env, err := l.qa.environment(d, "/qa/home")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/opt/bin:/usr/bin", "HOME=/qa/home", "TMPDIR=/qa/home/tmp", "OPENAI_API_KEY=sk-qa-key", "PROXY_CA=/etc/ca.pem"}
	if !slices.Equal(env, want) {
		t.Fatalf("the QA agent's environment is %v, want %v", env, want)
	}
	for _, name := range []string{"SISF_GITHUB_TOKEN", "EDGE_ADMIN_TOKEN", "ROOT_KEY", "STATE_DSN", "sisf_anything", "DOCKER_HOST", "HOME"} {
		l.qa.env = name
		if _, err := l.qa.environment(d, "/qa/home"); !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("passing %s to the QA agent: %v", name, err)
		}
	}
	l.qa.env = "NOT A NAME"
	if _, err := l.qa.environment(d, "/qa/home"); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a malformed variable name: %v", err)
	}
}

func TestTheQAAgentsDirectoriesLieApartFromTheTrustVolume(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(l *lifecycleFlags, d judgeDeps){
		"workspace inside the trust volume": func(l *lifecycleFlags, d judgeDeps) { l.qa.workspaceRoot = filepath.Join(d.trustDir, "qa") },
		"home is the trust volume":          func(l *lifecycleFlags, d judgeDeps) { l.qa.home = d.trustDir },
		"workspace holds the trust volume":  func(l *lifecycleFlags, d judgeDeps) { l.qa.workspaceRoot = filepath.Dir(d.trustDir) },
	} {
		l, d := judgeSetup(t, nil)
		change(l, d)
		if _, err := l.judge(context.Background(), d); !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("%s: %v", name, err)
		}
	}
	l, d := judgeSetup(t, nil)
	l.qa.home = "relative/home"
	if _, err := l.judge(context.Background(), d); !core.HasCode(err, core.CodeInvalidArgument) || strings.Contains(err.Error(), "relative/home") {
		t.Errorf("a relative home: %v", err)
	}
}

// The local execution backend is real; the QA agent is held to the same
// rule under it.
func TestTheLocalBackendRunsAQAAgentOnlyForTrustedCode(t *testing.T) {
	t.Parallel()
	local, err := execlocal.New(execlocal.Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	l, d := judgeSetup(t, nil)
	l.exec, d.docker, d.local = "local", nil, local
	if _, err := l.judge(context.Background(), d); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("an agent judge on local processes without trusted code: %v", err)
	}
	d.manifest.TrustedCodeOnly = true
	if _, err := l.judge(context.Background(), d); err != nil {
		t.Fatalf("an agent judge on local processes under trusted code: %v", err)
	}
}

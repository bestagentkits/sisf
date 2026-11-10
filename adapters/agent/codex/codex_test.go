package codex_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/codex"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
)

const sha = core.CommitSHA("0123456789abcdef0123456789abcdef01234567")

var route = core.ModelRoute{Provider: "openai", Model: "gpt-5.2-codex", Effort: "medium"}

func fixture(t testing.TB, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestCodexBackendConformance(t *testing.T) {
	t.Parallel()
	clitest.Conformance(t, clitest.Harness{
		Tool: "codex", Route: route, ForeignProvider: "anthropic",
		New: func(s clitest.Setup) (clitest.Subject, error) {
			return codex.New(codex.Config{Env: s.Env, Clock: s.Clock, ArtifactDir: s.ArtifactDir})
		},
		Success: []clitest.Step{{AwaitStdin: "*EOF*"}, {EmitFile: fixture(t, "success.jsonl")}},
		Failure: []clitest.Step{{AwaitStdin: "*EOF*"}, {EmitFile: fixture(t, "failure.jsonl")}},
		Leaky: func(token string) []clitest.Step {
			return []clitest.Step{
				{AwaitStdin: "*EOF*"},
				{Emit: `{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"calling with ` + token + `"}}`},
				{Emit: `{"type":"item.completed","item":{"id":"i2","type":"command_execution","command":"env","aggregated_output":"OPENAI_API_KEY=` + token + `","exit_code":1,"status":"failed"}}`},
				{Stderr: "auth failed " + token},
				{Emit: `{"type":"turn.failed","error":{"message":"401 ` + token + `"}}`},
			}
		},
	})
}

func task(role agent.Role, step core.StepKind) agent.Task {
	return agent.Task{
		RunID: "run-0001", IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 7}, Role: role, Step: step,
		Handoff: agent.Handoff{SoW: "Add GET /version.", DoD: "It returns the build revision."},
		Policy:  core.PolicyRefs{PolicySHA: sha, ReviewSHA: sha, DecisionSHA: sha, FeatureCatalogSHA: sha, ModelRoute: route},
		Limits:  agent.Limits{Timeout: 10 * time.Minute}, IdempotencyKey: "run-0001/start",
	}
}

func newBackend(t *testing.T, script clitest.Script, cfg codex.Config) *codex.Backend {
	t.Helper()
	bin := clitest.Install(t, "codex", script)
	cfg.Env = clitest.Env(bin)
	cfg.ArtifactDir = t.TempDir()
	b, err := codex.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTheCommandRunsExecModeEphemeralInTheWorkspaceWithThePromptOnStdin(t *testing.T) {
	t.Parallel()
	rec, ws := t.TempDir(), t.TempDir()
	b := newBackend(t, clitest.Script{Record: rec, Steps: []clitest.Step{{AwaitStdin: "*EOF*"}, {EmitFile: fixture(t, "success.jsonl")}}}, codex.Config{})
	ref, err := b.Start(context.Background(), task(agent.RoleCoder, core.StepCoding), agent.WorkspaceRef{Path: ws}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := b.Wait(context.Background(), ref); err != nil || status.Phase != agent.PhaseSucceeded {
		t.Fatalf("run %+v %v", status, err)
	}
	seen := clitest.Recorded(t, rec)
	if len(seen.Args) == 0 || seen.Args[0] != "exec" || seen.Args[len(seen.Args)-1] != "-" {
		t.Errorf("want 'exec ... -' reading the prompt from stdin, got %v", seen.Args)
	}
	for _, want := range [][]string{
		{"--json"}, {"--ephemeral"}, {"-C", ws}, {"-s", "workspace-write"}, {"-m", "gpt-5.2-codex"},
		{"-c", `model_reasoning_effort="medium"`}, {"-c", `approval_policy="never"`},
	} {
		if !containsRun(seen.Args, want) {
			t.Errorf("args %v lack %v", seen.Args, want)
		}
	}
	if !strings.Contains(seen.Stdin, "Add GET /version.") {
		t.Errorf("the prompt is not on stdin: %q", seen.Stdin)
	}
}

func TestOnlyAProvenSandboxRunsWithoutTheCLIsOwnSandbox(t *testing.T) {
	t.Parallel()
	for _, unrestricted := range []bool{false, true} {
		rec := t.TempDir()
		b := newBackend(t, clitest.Script{Record: rec, Steps: []clitest.Step{{AwaitStdin: "*EOF*"}, {EmitFile: fixture(t, "success.jsonl")}}}, codex.Config{Unrestricted: unrestricted})
		ref, err := b.Start(context.Background(), task(agent.RoleCoder, core.StepCoding), agent.WorkspaceRef{Path: t.TempDir()}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.Wait(context.Background(), ref); err != nil {
			t.Fatal(err)
		}
		seen := clitest.Recorded(t, rec)
		if got := containsRun(seen.Args, []string{"-s", "danger-full-access"}); got != unrestricted {
			t.Errorf("unrestricted=%v but args are %v", unrestricted, seen.Args)
		}
	}
}

func TestAnEvaluatorRunsInTheReadOnlySandboxEvenWhenUnrestricted(t *testing.T) {
	t.Parallel()
	for _, unrestricted := range []bool{false, true} {
		rec := t.TempDir()
		b := newBackend(t, clitest.Script{Record: rec, Steps: []clitest.Step{{AwaitStdin: "*EOF*"}, {EmitFile: fixture(t, "success.jsonl")}}}, codex.Config{Unrestricted: unrestricted})
		ref, err := b.Start(context.Background(), task(agent.RoleQA, core.StepQA), agent.WorkspaceRef{Path: t.TempDir()}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.Wait(context.Background(), ref); err != nil {
			t.Fatal(err)
		}
		seen := clitest.Recorded(t, rec)
		if !containsRun(seen.Args, []string{"-s", "read-only"}) {
			t.Errorf("unrestricted=%v: an evaluator may write: %v", unrestricted, seen.Args)
		}
	}
}

func TestTheStreamIsReadByEventTypeAndTolerantOfUnknownFields(t *testing.T) {
	t.Parallel()
	b := newBackend(t, clitest.Script{Steps: []clitest.Step{{AwaitStdin: "*EOF*"}, {EmitFile: fixture(t, "success.jsonl")}}}, codex.Config{})
	ref, err := b.Start(context.Background(), task(agent.RoleCoder, core.StepCoding), agent.WorkspaceRef{Path: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Wait(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	page, err := b.Events(context.Background(), ref, "")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[agent.EventKind]int{}
	var failedExit, changes bool
	for _, e := range page.Events {
		counts[e.Kind]++
		var p struct {
			ExitCode *int     `json:"exit_code"`
			Paths    []string `json:"paths"`
		}
		_ = json.Unmarshal(e.Payload, &p)
		failedExit = failedExit || (p.ExitCode != nil && *p.ExitCode == 0)
		changes = changes || slices.Contains(p.Paths, "add /work/version.go")
	}
	if counts[agent.EventToolCall] != 1 || counts[agent.EventToolResult] != 2 || counts[agent.EventMessage] != 1 || counts[agent.EventUsage] != 1 {
		t.Errorf("event counts %v: reasoning, web search and todo items are skipped", counts)
	}
	if !failedExit || !changes {
		t.Errorf("command exit code kept %v, file changes kept %v", failedExit, changes)
	}
	usage, _ := b.Usage(context.Background(), ref)
	if len(usage) != 1 || usage[0].InputTokens != 24763 || usage[0].OutputTokens != 122 || usage[0].Cost != 0 || usage[0].Verified {
		t.Errorf("usage %+v: want tokens only and no invented cost", usage)
	}
}

func TestAFailedTurnFailsTheRunAndAStreamErrorAloneDoesNot(t *testing.T) {
	t.Parallel()
	b := newBackend(t, clitest.Script{Steps: []clitest.Step{{AwaitStdin: "*EOF*"}, {EmitFile: fixture(t, "failure.jsonl")}}}, codex.Config{})
	ref, err := b.Start(context.Background(), task(agent.RoleCoder, core.StepCoding), agent.WorkspaceRef{Path: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	status, err := b.Wait(context.Background(), ref)
	if err != nil || status.Phase != agent.PhaseFailed || !strings.Contains(status.Detail, "stream disconnected") {
		t.Errorf("status %+v %v", status, err)
	}

	retry := newBackend(t, clitest.Script{Steps: []clitest.Step{{AwaitStdin: "*EOF*"},
		{Emit: `{"type":"error","message":"Reconnecting... 1/5"}`}, {Emit: `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`}}}, codex.Config{})
	ref, err = retry.Start(context.Background(), task(agent.RoleCoder, core.StepCoding), agent.WorkspaceRef{Path: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := retry.Wait(context.Background(), ref); err != nil || status.Phase != agent.PhaseSucceeded {
		t.Errorf("a retried stream error must not fail the run: %+v %v", status, err)
	}
}

func containsRun(args, run []string) bool {
	for i := 0; i+len(run) <= len(args); i++ {
		if slices.Equal(args[i:i+len(run)], run) {
			return true
		}
	}
	return false
}

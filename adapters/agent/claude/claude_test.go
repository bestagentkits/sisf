package claude_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/adapters/agent/claude"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
)

var route = core.ModelRoute{Provider: "anthropic", Model: "claude-sonnet-5-5", Effort: "low"}

func fixture(t testing.TB, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func harness(t *testing.T) clitest.Harness {
	return clitest.Harness{
		Tool:  "claude",
		Route: route, ForeignProvider: "openai",
		New: func(s clitest.Setup) (clitest.Subject, error) {
			return claude.New(claude.Config{Env: s.Env, Clock: s.Clock, ArtifactDir: s.ArtifactDir})
		},
		Success: []clitest.Step{{AwaitStdin: "*EOF*"}, {EmitFile: fixture(t, "success.jsonl")}},
		Failure: []clitest.Step{{AwaitStdin: "*EOF*"}, {EmitFile: fixture(t, "failure.jsonl")}},
		Leaky: func(token string) []clitest.Step {
			return []clitest.Step{
				{AwaitStdin: "*EOF*"},
				{Emit: `{"type":"assistant","message":{"content":[{"type":"text","text":"calling with ` + token + `"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"curl -H 'Authorization: Bearer ` + token + `'"}}]}}`},
				{Emit: `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"401 ` + token + `","is_error":true}]}}`},
				{Stderr: "auth failed " + token},
				{Emit: `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"401 ` + token + `"}`},
			}
		},
	}
}

func TestClaudeBackendConformance(t *testing.T) {
	t.Parallel()
	clitest.Conformance(t, harness(t))
}

func newBackend(t *testing.T, script clitest.Script, cfg claude.Config) *claude.Backend {
	t.Helper()
	bin := clitest.Install(t, "claude", script)
	cfg.Env = clitest.Env(bin)
	cfg.ArtifactDir = t.TempDir()
	b, err := claude.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func task(role agent.Role, step core.StepKind) agent.Task {
	return agent.Task{
		RunID: "run-0001", IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 7}, Role: role, Step: step,
		Handoff: agent.Handoff{SoW: "Add GET /version.", DoD: "It returns the build revision."},
		Policy:  core.PolicyRefs{PolicySHA: sha, ReviewSHA: sha, DecisionSHA: sha, FeatureCatalogSHA: sha, ModelRoute: route},
		Limits:  agent.Limits{Timeout: 600_000_000_000}, IdempotencyKey: "run-0001/start",
	}
}

const sha = core.CommitSHA("0123456789abcdef0123456789abcdef01234567")

func runAndRecord(t *testing.T, cfg claude.Config, tk agent.Task) clitest.Recording {
	t.Helper()
	rec := t.TempDir()
	b := newBackend(t, clitest.Script{Record: rec, Steps: []clitest.Step{{AwaitStdin: "*EOF*"}, {EmitFile: fixture(t, "success.jsonl")}}}, cfg)
	ref, err := b.Start(context.Background(), tk, agent.WorkspaceRef{Path: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := b.Wait(context.Background(), ref); err != nil || status.Phase != agent.PhaseSucceeded {
		t.Fatalf("run: %+v %v", status, err)
	}
	return clitest.Recorded(t, rec)
}

func TestTheCommandRunsPrintModeWithStreamJSONAndTheRoutedModel(t *testing.T) {
	t.Parallel()
	seen := runAndRecord(t, claude.Config{}, task(agent.RoleCoder, core.StepCoding))
	for _, want := range [][]string{
		{"-p"}, {"--output-format", "stream-json"}, {"--verbose"}, {"--no-session-persistence"},
		{"--model", "claude-sonnet-5-5"}, {"--effort", "low"}, {"--permission-prompts", "none"},
	} {
		if !containsRun(seen.Args, want) {
			t.Errorf("args %v lack %v", seen.Args, want)
		}
	}
}

func TestAnAgentWithoutASandboxIsRestrictedAndAnEvaluatorCannotEdit(t *testing.T) {
	t.Parallel()
	restricted := runAndRecord(t, claude.Config{AllowedTools: []string{"Bash(go test:*)"}}, task(agent.RoleCoder, core.StepCoding))
	if !containsRun(restricted.Args, []string{"--permission-mode", "acceptEdits"}) || slices.Contains(restricted.Args, "bypassPermissions") {
		t.Errorf("a restricted agent must not bypass permissions: %v", restricted.Args)
	}
	if i := slices.Index(restricted.Args, "--allowedTools"); i < 0 || !strings.Contains(restricted.Args[i+1], "Bash(go test:*)") {
		t.Errorf("allowed tools missing: %v", restricted.Args)
	}
	open := runAndRecord(t, claude.Config{Unrestricted: true}, task(agent.RoleCoder, core.StepCoding))
	if !containsRun(open.Args, []string{"--permission-mode", "bypassPermissions"}) {
		t.Errorf("an agent in a proven sandbox runs unrestricted: %v", open.Args)
	}
	qa := runAndRecord(t, claude.Config{}, task(agent.RoleQA, core.StepQA))
	if i := slices.Index(qa.Args, "--disallowedTools"); i < 0 || !strings.Contains(qa.Args[i+1], "Edit") || !strings.Contains(qa.Args[i+1], "Write") {
		t.Errorf("an evaluator must not edit files: %v", qa.Args)
	}
}

func TestTheStreamIsReadByEventTypeAndTolerantOfUnknownFields(t *testing.T) {
	t.Parallel()
	b := newBackend(t, clitest.Script{Steps: []clitest.Step{{AwaitStdin: "*EOF*"}, {EmitFile: fixture(t, "success.jsonl")}}}, claude.Config{})
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
	var tool struct{ Name, Input string }
	var result struct{ Content string }
	for _, e := range page.Events {
		counts[e.Kind]++
		if e.Kind == agent.EventToolCall && tool.Name == "" {
			_ = json.Unmarshal(e.Payload, &tool)
		}
		if e.Kind == agent.EventToolResult {
			_ = json.Unmarshal(e.Payload, &result)
		}
	}
	if counts[agent.EventToolCall] != 2 || counts[agent.EventToolResult] != 2 || counts[agent.EventUsage] != 1 || counts[agent.EventMessage] < 2 {
		t.Errorf("event counts %v: unknown stream events must be skipped, known ones all kept", counts)
	}
	if tool.Name != "Read" || !strings.Contains(tool.Input, "main.go") {
		t.Errorf("first tool call %+v", tool)
	}
	if !strings.Contains(result.Content, "ok") {
		t.Errorf("array-form tool result text not read: %+v", result)
	}
	usage, err := b.Usage(context.Background(), ref)
	if err != nil || len(usage) != 1 {
		t.Fatalf("usage %v %v", usage, err)
	}
	if got := usage[0]; got.InputTokens != 1420 || got.OutputTokens != 45 || got.Cost != 12300 || got.Verified {
		t.Errorf("usage %+v: want 120+300+1000 input tokens, 45 output, 12300 micros, unverified", got)
	}
}

func TestAnErrorResultFailsTheRunWithItsKind(t *testing.T) {
	t.Parallel()
	b := newBackend(t, clitest.Script{Steps: []clitest.Step{{AwaitStdin: "*EOF*"}, {EmitFile: fixture(t, "failure.jsonl")}}}, claude.Config{})
	ref, err := b.Start(context.Background(), task(agent.RoleCoder, core.StepCoding), agent.WorkspaceRef{Path: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	status, err := b.Wait(context.Background(), ref)
	if err != nil || status.Phase != agent.PhaseFailed || !strings.Contains(status.Detail, "error_max_turns") {
		t.Errorf("status %+v %v", status, err)
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

func TestTheResultEventIsTheExplicitFinalReport(t *testing.T) {
	t.Parallel()
	b := newBackend(t, clitest.Script{Steps: []clitest.Step{{AwaitStdin: "*EOF*"}, {EmitFile: fixture(t, "success.jsonl")}}}, claude.Config{})
	ref, err := b.Start(context.Background(), task(agent.RoleQA, core.StepQA), agent.WorkspaceRef{Path: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Wait(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	report, err := b.FinalReport(context.Background(), ref)
	if err != nil || !report.HasResult || !report.Complete || report.Result != "Added GET /version and the tests pass." {
		t.Fatalf("final report %+v, %v", report, err)
	}
}

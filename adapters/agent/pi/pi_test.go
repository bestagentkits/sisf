package pi_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/pi"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
)

const sha = core.CommitSHA("0123456789abcdef0123456789abcdef01234567")

var route = core.ModelRoute{Provider: "google", Model: "gemini-3-pro", Effort: "medium"}

func fixture(t testing.TB, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// session is what Pi does around a run: read the prompt command, print the
// events, answer the statistics request, and exit when stdin closes.
func session(t testing.TB, events ...clitest.Step) []clitest.Step {
	steps := []clitest.Step{{AwaitStdin: `"type":"prompt"`}}
	steps = append(steps, events...)
	return append(steps,
		clitest.Step{AwaitStdin: "get_session_stats"},
		clitest.Step{EmitFile: fixture(t, "stats.jsonl")},
		clitest.Step{AwaitStdin: "*EOF*"},
	)
}

func TestPiBackendConformance(t *testing.T) {
	t.Parallel()
	clitest.Conformance(t, clitest.Harness{
		Tool: "pi", Route: route,
		New: func(s clitest.Setup) (clitest.Subject, error) {
			return pi.New(pi.Config{Env: s.Env, Clock: s.Clock, ArtifactDir: s.ArtifactDir})
		},
		Success: session(t, clitest.Step{EmitFile: fixture(t, "success.jsonl")}),
		Failure: session(t, clitest.Step{EmitFile: fixture(t, "failure.jsonl")}),
		Leaky: func(token string) []clitest.Step {
			return session(t,
				clitest.Step{Emit: `{"id":"factory-prompt","type":"response","command":"prompt","success":true,"data":{"disposition":"started"}}`},
				clitest.Step{Emit: `{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":{"content":[{"type":"text","text":"OPENAI_API_KEY=` + token + `"}]},"isError":true}`},
				clitest.Step{Stderr: "auth failed " + token},
				clitest.Step{Emit: `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"calling with ` + token + `"}],"stopReason":"error","errorMessage":"401 ` + token + `"}}`},
				clitest.Step{Emit: `{"type":"agent_settled"}`},
			)
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

func run(t *testing.T, script clitest.Script, tk agent.Task) (*pi.Backend, agent.RunRef, agent.Status) {
	t.Helper()
	bin := clitest.Install(t, "pi", script)
	b, err := pi.New(pi.Config{Env: clitest.Env(bin), ArtifactDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := b.Start(context.Background(), tk, agent.WorkspaceRef{Path: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	status, err := b.Wait(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return b, ref, status
}

func TestPiRunsInRPCModeWithTheRouteAndTheTaskAsAPromptCommand(t *testing.T) {
	t.Parallel()
	rec := t.TempDir()
	_, _, status := run(t, clitest.Script{Record: rec, Steps: session(t, clitest.Step{EmitFile: fixture(t, "success.jsonl")})}, task(agent.RoleCoder, core.StepCoding))
	if status.Phase != agent.PhaseSucceeded {
		t.Fatalf("status %+v", status)
	}
	seen := clitest.Recorded(t, rec)
	if !slices.Equal(seen.Args, []string{"--mode", "rpc", "--no-session", "--provider", "google", "--model", "gemini-3-pro", "--thinking", "medium"}) {
		t.Errorf("args %v", seen.Args)
	}
	lines := strings.Split(strings.TrimSpace(seen.Stdin), "\n")
	var prompt struct{ ID, Type, Message string }
	if err := json.Unmarshal([]byte(lines[0]), &prompt); err != nil || prompt.Type != "prompt" || !strings.Contains(prompt.Message, "Add GET /version.") {
		t.Errorf("first command %q: %v", lines[0], err)
	}
	if !strings.Contains(seen.Stdin, "get_session_stats") {
		t.Error("the backend must ask for session statistics before closing stdin")
	}
	for _, arg := range seen.Args {
		if strings.Contains(arg, "GET /version") {
			t.Errorf("the task must not be a process argument: %q", arg)
		}
	}
}

func TestAnEvaluatorGetsNoEditingTools(t *testing.T) {
	t.Parallel()
	rec := t.TempDir()
	run(t, clitest.Script{Record: rec, Steps: session(t, clitest.Step{EmitFile: fixture(t, "success.jsonl")})}, task(agent.RoleQA, core.StepQA))
	seen := clitest.Recorded(t, rec)
	if i := slices.Index(seen.Args, "--exclude-tools"); i < 0 || seen.Args[i+1] != "edit,write" {
		t.Errorf("args %v", seen.Args)
	}
}

func TestEventsAreReadByTypeAndUsageComesFromTheSessionStatistics(t *testing.T) {
	t.Parallel()
	b, ref, status := run(t, clitest.Script{Steps: session(t, clitest.Step{EmitFile: fixture(t, "success.jsonl")})}, task(agent.RoleCoder, core.StepCoding))
	if status.Phase != agent.PhaseSucceeded {
		t.Fatalf("status %+v", status)
	}
	page, err := b.Events(context.Background(), ref, "")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[agent.EventKind]int{}
	for _, e := range page.Events {
		counts[e.Kind]++
	}
	if counts[agent.EventToolCall] != 1 || counts[agent.EventToolResult] != 1 || counts[agent.EventMessage] != 2 || counts[agent.EventUsage] != 1 {
		t.Errorf("event counts %v: user messages, thinking blocks and deltas are skipped", counts)
	}
	usage, _ := b.Usage(context.Background(), ref)
	if len(usage) != 1 || usage[0].InputTokens != 9500 || usage[0].OutputTokens != 1200 || usage[0].Cost != 450000 || usage[0].Verified {
		t.Errorf("usage %+v: want 5000+4000+500 input, 1200 output, 450000 micros, unverified", usage)
	}
}

func TestAnErrorStopReasonFailsTheRun(t *testing.T) {
	t.Parallel()
	_, _, status := run(t, clitest.Script{Steps: session(t, clitest.Step{EmitFile: fixture(t, "failure.jsonl")})}, task(agent.RoleCoder, core.StepCoding))
	if status.Phase != agent.PhaseFailed || !strings.Contains(status.Detail, "529 overloaded") {
		t.Errorf("status %+v", status)
	}
}

func TestARejectedOrHandledPromptEndsTheRunAndClosesInput(t *testing.T) {
	t.Parallel()
	for name, line := range map[string]string{
		"rejected": `{"id":"factory-prompt","type":"response","command":"prompt","success":false,"error":"Model not found"}`,
		"handled":  `{"id":"factory-prompt","type":"response","command":"prompt","success":true,"data":{"disposition":"handled"}}`,
	} {
		_, _, status := run(t, clitest.Script{Steps: []clitest.Step{{AwaitStdin: `"type":"prompt"`}, {Emit: line}, {AwaitStdin: "*EOF*"}}}, task(agent.RoleCoder, core.StepCoding))
		if status.Phase != agent.PhaseFailed || status.Detail == "" {
			t.Errorf("%s: status %+v, want failed (and the process must see stdin close)", name, status)
		}
	}
}

func TestAnExtensionDialogIsCancelledNotApproved(t *testing.T) {
	t.Parallel()
	rec := t.TempDir()
	_, _, status := run(t, clitest.Script{Record: rec, Steps: session(t,
		clitest.Step{Emit: `{"id":"factory-prompt","type":"response","command":"prompt","success":true,"data":{"disposition":"started"}}`},
		clitest.Step{Emit: `{"type":"extension_ui_request","id":"ui-1","method":"confirm","title":"Allow dangerous command?"}`},
		clitest.Step{Emit: `{"type":"extension_ui_request","id":"ui-2","method":"notify","message":"fyi"}`},
		clitest.Step{AwaitStdin: "extension_ui_response"},
		clitest.Step{Emit: `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"stopReason":"stop"}}`},
		clitest.Step{Emit: `{"type":"agent_settled"}`},
	)}, task(agent.RoleCoder, core.StepCoding))
	if status.Phase != agent.PhaseSucceeded {
		t.Fatalf("status %+v", status)
	}
	seen := clitest.Recorded(t, rec)
	if !strings.Contains(seen.Stdin, `"cancelled":true`) || !strings.Contains(seen.Stdin, `"id":"ui-1"`) || strings.Contains(seen.Stdin, `"confirmed":true`) {
		t.Errorf("the dialog was not cancelled:\n%s", seen.Stdin)
	}
	if strings.Contains(seen.Stdin, `"id":"ui-2"`) {
		t.Error("a fire-and-forget notification must not be answered")
	}
}

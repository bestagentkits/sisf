//go:build unix

package acceptance

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/claude"
	"github.com/bestagentkits/sisf/adapters/agent/codex"
	"github.com/bestagentkits/sisf/cmd/factoryd/lifecycle"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/qa/agentjudge"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
)

// greeted is examples/hello-app/main.go after the coding agent's change.
const greeted = "package main\n\nfunc main() { println(\"hello, world\") }\n"

// An issue goes the whole way with real agent adapters on both sides: a
// scripted Claude CLI writes the change in a fresh workspace, and a
// scripted Codex CLI, as the independent QA agent, judges the built
// candidate from the supervisor's materials. Its scores reach a receipt
// the supervisor signs, and the candidate is released.
func TestAnAgentJudgedCandidateIsReleasedWithASignedReceipt(t *testing.T) {
	t.Parallel()
	qaRecord := t.TempDir()
	f := agentFactory(t, []clitest.Script{{Record: qaRecord, Steps: qaRun(26)}})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)

	settle(t, f, number, core.StateClosed)
	wantJourneyEnds(t, f, number, fullLifecycle)
	result := qaResult(t, f, number, 1)
	receipt := result.Receipt
	if result.Verdict != stages.VerdictPassed || receipt.Score != 96 || receipt.Signature.IsZero() ||
		receipt.QAModelRoute.Provider != "openai" || receipt.CodingModelRoute.Provider != "anthropic" || receipt.SameModelRoute {
		t.Fatalf("the QA result %+v", result)
	}
	for _, check := range receipt.Checks {
		if check.Kind == core.CheckRubric && (!strings.HasPrefix(check.EvidenceRef, "file://") || check.Detail == "") {
			t.Fatalf("the rubric check %s cites no judgment: %+v", check.Name, check)
		}
	}
	// The QA agent judged the coder's own patch, read-only, from the
	// approved scope, and never in the coder's workspace.
	seen := clitest.Recorded(t, qaRecord)
	mustHave(t, seen.Stdin, "+func main() { println(\"hello, world\") }", "The greeting is too terse.", "UNTRUSTED DATA")
	if !slices.Contains(seen.Args, "read-only") {
		t.Fatalf("the QA agent was not read-only: %v", seen.Args)
	}
}

// A judgment below the pass score is a failed evaluation: the issue goes
// back to development with the next attempt, whose candidate the QA agent
// passes.
func TestAFailingAgentJudgmentRetriesTheCandidate(t *testing.T) {
	t.Parallel()
	f := agentFactory(t, []clitest.Script{{Steps: qaRun(0)}, {Steps: qaRun(30)}})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)

	settle(t, f, number, core.StateClosed)
	journey := f.Journey(number)
	if i := slices.Index(journey, core.StateQAEvaluation); i < 0 || journey[i+1] != core.StateDeveloping {
		t.Fatalf("the failed judgment did not send the issue back to development: %v", journey)
	}
	if first := qaResult(t, f, number, 1); first.Verdict != stages.VerdictFailed || first.Receipt.Score != 70 || first.Receipt.Signature.IsZero() {
		t.Fatalf("the first evaluation: %+v", first)
	}
	if record := f.Record(number); record.Attempt != 2 {
		t.Fatalf("the issue closed at attempt %d, want 2", record.Attempt)
	}
}

// A QA agent whose judgment breaks the contract issues no receipt: the
// evaluation fails and the run goes back to development.
func TestAMalformedAgentJudgmentIssuesNoReceipt(t *testing.T) {
	t.Parallel()
	broken := codexStream(agentjudge.OpenMarker + `{"version":1,"scores":{}}` + agentjudge.CloseMarker)
	f := agentFactory(t, []clitest.Script{{Steps: broken}, {Steps: qaRun(30)}})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)

	settle(t, f, number, core.StateClosed)
	first := qaResult(t, f, number, 1)
	if first.Verdict != stages.VerdictFailed || !first.Receipt.Signature.IsZero() || !strings.Contains(first.Reason, "no receipt") {
		t.Fatalf("a malformed judgment: %+v", first)
	}
}

// agentFactory builds a harness factory whose coder is the Claude adapter
// on a scripted CLI and whose judge is the agent judge on the Codex
// adapter, one scripted CLI per evaluation in order.
func agentFactory(t *testing.T, judgments []clitest.Script) *harness.Factory {
	t.Helper()
	coderBin := clitest.Install(t, "claude", clitest.Script{Steps: []clitest.Step{
		{AwaitStdin: "*EOF*"},
		{WriteFile: map[string]string{"examples/hello-app/main.go": greeted}},
		{Emit: `{"type":"system","subtype":"init","model":"mid"}`},
		{Emit: `{"type":"assistant","message":{"content":[{"type":"text","text":"Changed the greeting."}]}}`},
		{Emit: `{"type":"result","subtype":"success","is_error":false,"result":"Changed the greeting.","total_cost_usd":0.01,"usage":{"input_tokens":100,"output_tokens":20}}`},
	}})
	artifacts := t.TempDir()
	coder, err := claude.New(claude.Config{Env: clitest.Env(coderBin), ArtifactDir: artifacts})
	if err != nil {
		t.Fatal(err)
	}
	judges := make([]qa.Judge, 0, len(judgments))
	return harness.New(t, harness.Options{
		Coder: usageOf{Backend: &lifecycle.FreshWorkspaces{Backend: finished{coder}, Root: t.TempDir(), Provision: seedRepository},
			usage: coder},
		CoderArtifacts: lifecycle.FileArtifacts{Root: artifacts},
		QAJudge: func(materials agentjudge.MaterialsSource) (qa.Judge, error) {
			for _, script := range judgments {
				bin := clitest.Install(t, "codex", script)
				backend, err := codex.New(codex.Config{Env: clitest.Env(bin, "OPENAI_API_KEY=sk-qa-credential-of-the-test"), ArtifactDir: t.TempDir()})
				if err != nil {
					return nil, err
				}
				judge, err := agentjudge.New(agentjudge.Config{Agent: backend, Materials: materials, WorkspaceRoot: t.TempDir(),
					Timeout: time.Minute, Usage: stages.JudgeUsage{State: materials.(stages.CandidateMaterials).State}})
				if err != nil {
					return nil, err
				}
				judges = append(judges, judge)
			}
			return &inTurn{judges: judges}, nil
		},
	})
}

// settle runs the factory until issue number reaches want.
func settle(t *testing.T, f *harness.Factory, number int, want core.State) {
	t.Helper()
	for range 12 {
		f.Cycle(1)
		if f.Record(number).State == want {
			return
		}
	}
	t.Fatalf("issue %d is %s after 12 rounds, want %s; journey %v", number, f.Record(number).State, want, f.Journey(number))
}

// qaResult reads what the supervisor concluded of one evaluator attempt.
func qaResult(t *testing.T, f *harness.Factory, number, attempt int) stages.QAResult {
	t.Helper()
	var result stages.QAResult
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		result, err = stages.ReadQAResult(ctx, tx, f.Record(number).RunID, attempt)
		return err
	})
	if err != nil {
		t.Fatalf("read the QA result of attempt %d: %v", attempt, err)
	}
	return result
}

// qaRun is a Codex run whose final message scores correctness at
// correctness and every other criterion in full.
func qaRun(correctness int) []clitest.Step {
	scores := map[string]any{}
	for _, category := range qa.Categories() {
		points := qa.DefaultRubric().Weights[category]
		if category == qa.CategoryCorrectness {
			points = correctness
		}
		scores[string(category)] = map[string]any{"points": points, "evidence": "examples/hello-app/main.go: the greeting changed"}
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "scores": scores, "hard_gate_observations": []any{}, "summary": "judged"})
	return codexStream(agentjudge.OpenMarker + "\n" + string(raw) + "\n" + agentjudge.CloseMarker)
}

// codexStream is `codex exec --json` answering with one final message.
func codexStream(final string) []clitest.Step {
	message, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{"id": "m1", "type": "agent_message", "text": final}})
	return []clitest.Step{
		{AwaitStdin: "*EOF*"},
		{Emit: `{"type":"thread.started","thread_id":"th-1"}`},
		{Emit: `{"type":"turn.started"}`},
		{Emit: string(message)},
		{Emit: `{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":120}}`},
	}
}

// inTurn hands each evaluation to the next judge, the last one repeating.
type inTurn struct {
	mu     sync.Mutex
	judges []qa.Judge
	next   int
}

func (j *inTurn) Score(ctx context.Context, req qa.JudgeRequest) (qa.Scorecard, error) {
	j.mu.Lock()
	judge := j.judges[min(j.next, len(j.judges)-1)]
	j.next++
	j.mu.Unlock()
	return judge.Score(ctx, req)
}

// usageOf is the wrapped coder that still reports its adapter's usage, as
// the agent zone wires a real coding agent.
type usageOf struct {
	agent.Backend
	usage agent.UsageReporter
}

func (u usageOf) Usage(ctx context.Context, ref agent.RunRef) ([]core.Usage, error) {
	return u.usage.Usage(ctx, ref)
}

// finished starts a run and returns once it has ended, so the harness's
// next dispatcher round sees it done without waiting on the wall clock.
//
// The fake GitHub's commit IDs are content hashes, not git objects, so
// seedRepository cannot check the workspace out at the base the dispatcher
// names; its seed commit, which holds the same files, stands in for it and
// the agent's patch is taken against the workspace's HEAD.
type finished struct{ agentjudge.Runner }

func (f finished) Start(ctx context.Context, task agent.Task, ws agent.WorkspaceRef, scopes []string) (agent.RunRef, error) {
	ws.BaseSHA = ""
	ref, err := f.Runner.Start(ctx, task, ws, scopes)
	if err != nil {
		return ref, err
	}
	wait, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	_, err = f.Wait(wait, ref)
	return ref, err
}

// seedRepository fills a coding workspace with the repository's seed files
// as one commit, the base the agent's patch is taken against.
func seedRepository(ctx context.Context, dir string, _ core.CommitSHA) error {
	for path, content := range harness.SeedFiles {
		target := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			return err
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=seed", "-c", "user.email=seed@example.invalid", "commit", "-q", "-m", "seed"}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			return core.NewError(core.CodeUnavailable, "seed the workspace: "+string(out)).WithCause(err)
		}
	}
	return nil
}

// mustHave fails unless s holds every part.
func mustHave(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(s, p) {
			t.Fatalf("the QA agent's prompt lacks %q", p)
		}
	}
}

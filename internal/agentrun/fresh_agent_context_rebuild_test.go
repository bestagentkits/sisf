package agentrun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/claude"
	"github.com/bestagentkits/sisf/internal/agentrun"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// conversationOnlyInAgentA is said by the first agent and nowhere else. If
// it ever reaches the second agent or the stored state, a conversation was
// carried over.
const conversationOnlyInAgentA = "CONVERSATION-ONLY-IN-AGENT-A: I chose a mutex because of the race I saw in the router"

func assistantLine(text string) string {
	raw, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}, {"type": "tool_use", "id": "toolu_agent_a_1", "name": "Edit", "input": map[string]string{"file_path": "part1.go"}}},
	}})
	return string(raw)
}

// newProcess builds everything a new factory process would: its own store
// handle, router, controller and Claude backend over its own stand-in program.
// Nothing is shared with another process except the database and the workspace.
func newProcess(t *testing.T, db *sqlstoretest.Database, script clitest.Script) (*agentrun.Controller, *claude.Backend) {
	t.Helper()
	script.Version = "2.1.322 (stub)"
	bin := clitest.Install(t, "claude", script)
	backend, err := claude.New(claude.Config{Env: clitest.Env(bin), Clock: db.Clock, ArtifactDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	router, err := agentrun.NewRouter(routes())
	if err != nil {
		t.Fatal(err)
	}
	ctl, err := agentrun.New(agentrun.Config{
		State: db.Open(t, core.ZoneAgent), Router: router, Clock: db.Clock,
		Backends: map[string]agent.Backend{"anthropic": backend},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctl, backend
}

func initRepo(t *testing.T, dir string) core.CommitSHA {
	t.Helper()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(out.String())
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "base")
	return core.CommitSHA(run("rev-parse", "HEAD"))
}

// A run is interrupted in the middle of the work. A fresh agent, a new
// process with no chat history, is started from the stored handoff summary
// and the stored state alone, and finishes the work. Nothing of the first
// agent's conversation is required or passed.
func TestAFreshAgentContinuesAnInterruptedRunFromTheStoredHandoffAndStateAlone(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		workspace := t.TempDir()
		base := initRepo(t, workspace)
		recA, recB := t.TempDir(), t.TempDir()

		// Process A: the first agent does part of the work, says something
		// that exists only in its conversation, and then never finishes.
		ctlA, backendA := newProcess(t, db, clitest.Script{Record: recA, Steps: []clitest.Step{
			{AwaitStdin: "*EOF*"},
			{WriteFile: map[string]string{"version.go": "package main\n\n// partial: the handler is not written yet\n"}},
			{Emit: assistantLine(conversationOnlyInAgentA)},
			{Hang: true},
		}})
		job := agentrun.Job{
			RunID: "run-interrupted", Issue: issue, Step: core.StepCoding,
			Policy: core.PolicyRefs{PolicySHA: sha, ReviewSHA: sha, DecisionSHA: sha, FeatureCatalogSHA: sha},
			Handoff: agent.Handoff{
				SoW:       "Add GET /version that returns the build revision.",
				DoD:       "GET /version returns the build revision and a test proves it.",
				Decisions: []string{"Use net/http from the standard library only."},
				Evidence:  []string{"Issue acme/app#7 is approved and in scope."},
			},
			Limits:    agent.Limits{Timeout: time.Hour},
			Workspace: agent.WorkspaceRef{ID: "ws", Path: workspace, BaseSHA: base},
		}
		started, err := ctlA.Start(ctx, job)
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		agentA := *started.Current().Agent
		clitest.WaitForFile(t, filepath.Join(workspace, "version.go"))
		waitForEvent(t, backendA, agentA, "CONVERSATION-ONLY-IN-AGENT-A")

		// The interruption: the process is killed and everything held in
		// memory by process A is gone. Only the store and the workspace remain.
		pidA := clitest.Recorded(t, recA).Pid
		if err := backendA.Cancel(ctx, agentA); err != nil {
			t.Fatalf("interrupt: %v", err)
		}
		// From here nothing of process A is used again.

		// Process B: a new process, a new binary invocation, no memory of A.
		ctlB, backendB := newProcess(t, db, clitest.Script{Record: recB, Steps: []clitest.Step{
			{AwaitStdin: "*EOF*"},
			{WriteFile: map[string]string{"version_test.go": "package main\n\n// the test that proves GET /version\n"}},
			{Emit: `{"type":"assistant","message":{"content":[{"type":"text","text":"Finished the handler and the test."}]}}`},
			{Emit: `{"type":"result","subtype":"success","is_error":false,"result":"done","total_cost_usd":0.01,"usage":{"input_tokens":50,"output_tokens":10}}`},
		}})
		if _, err := backendB.Status(ctx, agentA); !core.HasCode(err, core.CodeNotFound) {
			t.Fatalf("the new process must know nothing of the first agent's run: %v", err)
		}
		interrupted, err := ctlB.Observe(ctx, job.RunID)
		if err != nil || interrupted.Status != agentrun.StatusInterrupted {
			t.Fatalf("the stored run must be seen as interrupted: %+v %v", interrupted, err)
		}
		resumed, err := ctlB.Resume(ctx, job.RunID)
		if err != nil {
			t.Fatalf("resume: %v", err)
		}
		agentB := *resumed.Current().Agent
		if status, err := backendB.Wait(ctx, agentB); err != nil || status.Phase != agent.PhaseSucceeded {
			t.Fatalf("the fresh agent did not finish: %+v %v", status, err)
		}
		done, err := ctlB.Observe(ctx, job.RunID)
		if err != nil || done.Status != agentrun.StatusAwaitingQA {
			t.Fatalf("after the fresh agent finished: %+v %v", done, err)
		}

		// 1. The fresh agent was told the scope, the definition of done, the
		// decisions and the evidence, and nothing else of the past.
		seenB := clitest.Recorded(t, recB)
		for _, want := range []string{
			"Add GET /version that returns the build revision.",
			"GET /version returns the build revision and a test proves it.",
			"Use net/http from the standard library only.",
			"Issue acme/app#7 is approved and in scope.",
			"Attempt 1 was interrupted",
			"unfinished changes remain in the workspace",
		} {
			if !strings.Contains(seenB.Stdin, want) {
				t.Errorf("the fresh agent was not told %q; it was given:\n%s", want, seenB.Stdin)
			}
		}
		for _, leaked := range []string{"CONVERSATION-ONLY-IN-AGENT-A", "toolu_agent_a_1", "mutex"} {
			if strings.Contains(seenB.Stdin, leaked) {
				t.Errorf("the previous agent's conversation reached the fresh agent: %q", leaked)
			}
		}
		for _, arg := range seenB.Args {
			switch arg {
			case "--resume", "-r", "--continue", "-c", "--session-id", "--fork-session":
				t.Errorf("the fresh agent was told to continue a session: %q", arg)
			}
		}
		if !contains(seenB.Args, "--no-session-persistence") {
			t.Errorf("no session may be kept for a later run to continue: %v", seenB.Args)
		}

		// 2. It is a new process, and the old one is gone.
		if seenB.Pid == 0 || seenB.Pid == pidA {
			t.Errorf("the fresh agent must be a new process: pid %d, first agent pid %d", seenB.Pid, pidA)
		}
		if err := syscall.Kill(pidA, 0); err != syscall.ESRCH {
			t.Errorf("the interrupted agent must be gone, kill(0) returned %v", err)
		}

		// 3. The stored state carries the handoff and the observations, and
		// none of the conversation.
		stored, err := ctlB.State(ctx, job.RunID)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(stored)
		for _, leaked := range []string{"CONVERSATION-ONLY-IN-AGENT-A", "toolu_agent_a_1"} {
			if strings.Contains(string(raw), leaked) {
				t.Errorf("the stored state holds the previous conversation: %q", leaked)
			}
		}
		if len(stored.Attempts) != 2 || stored.Attempts[1].Rung != "resume" || stored.Ladder.Used != 0 {
			t.Errorf("attempts %+v ladder %+v: want a second attempt that is a resume and no escalation spent", stored.Attempts, stored.Ladder)
		}
		if stored.Attempts[0].HandoffDigest == stored.Attempts[1].HandoffDigest || stored.Attempts[1].HandoffDigest == "" {
			t.Errorf("each attempt records the handoff it started from: %q then %q", stored.Attempts[0].HandoffDigest, stored.Attempts[1].HandoffDigest)
		}
		for _, a := range stored.Attempts {
			if a.ToolVersion != "2.1.322 (stub)" {
				t.Errorf("attempt %d did not record the CLI version: %q", a.Number, a.ToolVersion)
			}
		}
		if stored.Attempts[0].Outcome != agentrun.OutcomeInterrupted || stored.Attempts[1].Outcome != agentrun.OutcomeSubmitted {
			t.Errorf("outcomes %q then %q", stored.Attempts[0].Outcome, stored.Attempts[1].Outcome)
		}

		// 4. The work continued correctly: the candidate holds the first
		// agent's partial file and the second agent's completion.
		var patch string
		for _, a := range done.Current().Artifacts {
			if a.Kind == agent.ArtifactPatch {
				data, err := os.ReadFile(strings.TrimPrefix(a.URI, "file://"))
				if err != nil {
					t.Fatal(err)
				}
				patch = string(data)
			}
		}
		if !strings.Contains(patch, "b/version.go") || !strings.Contains(patch, "b/version_test.go") {
			t.Errorf("the candidate must combine both agents' work:\n%s", patch)
		}
		if usage := done.Current().Usage; len(usage) != 1 || usage[0].InputTokens != 50 {
			t.Errorf("usage of the fresh agent %+v", usage)
		}
		passed, err := ctlB.ReportQA(ctx, job.RunID, agentrun.Verdict{Passed: true, Evidence: []string{"qa-receipt-1"}})
		if err != nil || passed.Status != agentrun.StatusPassed {
			t.Errorf("after QA: %+v %v", passed, err)
		}
	})
}

// waitForEvent waits until an event of the run mentions text.
func waitForEvent(t *testing.T, b *claude.Backend, ref agent.RunRef, text string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		page, err := b.Events(context.Background(), ref, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Events {
			if strings.Contains(string(e.Payload), text) {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no event mentioned %q", text)
}

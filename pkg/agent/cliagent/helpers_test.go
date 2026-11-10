package cliagent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
)

const sha = core.CommitSHA("0123456789abcdef0123456789abcdef01234567")

var issue = core.IssueRef{Owner: "acme", Repo: "app", Number: 7}

// lineProtocol is a minimal protocol: one JSON object per line, typed
// "msg", "usage", "done" or "fail".
type lineProtocol struct{}

func (lineProtocol) Tool() string { return "stub" }

func (lineProtocol) Invocation(in cliagent.Input) (cliagent.Invocation, error) {
	return cliagent.Invocation{Args: []string{"run"}}, nil
}

func (lineProtocol) NewSession(in cliagent.Input) cliagent.Session {
	return &lineSession{prompt: in.Prompt}
}

type lineSession struct{ prompt string }

func (s *lineSession) Begin() cliagent.Update {
	return cliagent.Update{Stdin: []byte(s.prompt), CloseStdin: true}
}

func (s *lineSession) Line(line []byte) cliagent.Update {
	kind, _, ok := cliagent.Decode(line)
	if !ok {
		return cliagent.Update{}
	}
	var body struct {
		Text   string `json:"text"`
		In     int64  `json:"in"`
		Out    int64  `json:"out"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(line, &body)
	switch kind {
	case "msg":
		return cliagent.Update{Events: []cliagent.Emit{{Kind: agent.EventMessage, Payload: map[string]string{"text": body.Text}}}}
	case "usage":
		return cliagent.Update{Usage: &cliagent.Usage{InputTokens: body.In, OutputTokens: body.Out, Cost: 1500}}
	case "turn":
		return cliagent.Update{Turn: true}
	case "done":
		return cliagent.Update{Final: &cliagent.Outcome{OK: true}}
	case "fail":
		return cliagent.Update{Final: &cliagent.Outcome{OK: false, Detail: body.Detail}}
	}
	return cliagent.Update{}
}

func newTask(runID core.RunID, key string) agent.Task {
	return agent.Task{
		RunID: runID, IssueID: issue, Role: agent.RoleCoder, Step: core.StepCoding,
		Handoff: agent.Handoff{SoW: "Add GET /version.", DoD: "GET /version returns the build revision."},
		Policy: core.PolicyRefs{PolicySHA: sha, ReviewSHA: sha, DecisionSHA: sha, FeatureCatalogSHA: sha,
			ModelRoute: core.ModelRoute{Provider: "anthropic", Model: "mid", Effort: "low"}},
		Limits:         agent.Limits{Timeout: time.Hour},
		IdempotencyKey: key,
	}
}

type rig struct {
	b     *cliagent.Backend
	clock *clitest.Clock
	ws    agent.WorkspaceRef
	art   string
}

// newRig installs a stand-in named "stub" running script and a backend for it.
func newRig(t *testing.T, script clitest.Script, extraEnv ...string) *rig {
	t.Helper()
	bin := clitest.Install(t, "stub", script)
	clock := clitest.NewClock(time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC))
	art := t.TempDir()
	b, err := cliagent.New(cliagent.Config{
		Protocol: lineProtocol{}, Env: clitest.Env(bin, extraEnv...), Clock: clock, ArtifactDir: art,
	})
	if err != nil {
		t.Fatalf("new backend: %v", err)
	}
	return &rig{b: b, clock: clock, ws: agent.WorkspaceRef{ID: "ws", Path: t.TempDir()}, art: art}
}

func (r *rig) start(t *testing.T, task agent.Task) agent.RunRef {
	t.Helper()
	ref, err := r.b.Start(context.Background(), task, r.ws, []string{"repo:read"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return ref
}

func (r *rig) wait(t *testing.T, ref agent.RunRef) agent.Status {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status, err := r.b.Wait(ctx, ref)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	return status
}

func (r *rig) events(t *testing.T, ref agent.RunRef) []agent.Event {
	t.Helper()
	page, err := r.b.Events(context.Background(), ref, "")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return page.Events
}

func initGitRepo(t *testing.T, dir string) core.CommitSHA {
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
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "base")
	return core.CommitSHA(run("rev-parse", "HEAD"))
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

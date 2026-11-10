package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
)

// recordingAgent remembers the workspace each start was given.
type recordingAgent struct {
	*fake.Backend
	paths []string
}

func (r *recordingAgent) Start(ctx context.Context, task agent.Task, ws agent.WorkspaceRef, scopes []string) (agent.RunRef, error) {
	r.paths = append(r.paths, ws.Path)
	return r.Backend.Start(ctx, task, ws, scopes)
}

func coderTask(run core.RunID, key string) agent.Task {
	return agent.Task{
		RunID: run, IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 1}, Role: agent.RoleCoder, Step: core.StepCoding,
		Handoff: agent.Handoff{SoW: "s", DoD: "d"}, Policy: testPolicy(), Limits: agent.Limits{Timeout: 60e9}, IdempotencyKey: key,
	}
}

func testPolicy() core.PolicyRefs {
	sha := core.CommitSHA("0123456789abcdef0123456789abcdef01234567")
	return core.PolicyRefs{PolicySHA: sha, ReviewSHA: sha, DecisionSHA: sha, FeatureCatalogSHA: sha,
		ModelRoute: core.ModelRoute{Provider: "anthropic", Model: "mid"}}
}

func TestAResumedRunNeverSharesTheWorkspaceAnOrphanedAgentHolds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	inner, err := fake.New(fake.Config{})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingAgent{Backend: inner}
	first := &FreshWorkspaces{Backend: rec, Root: root}
	if _, err := first.Start(ctx, coderTask("run-1", "run-1/attempt/1"), agent.WorkspaceRef{ID: "run-1"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Start(ctx, coderTask("run-1", "run-1/attempt/1"), agent.WorkspaceRef{ID: "run-1"}, nil); err != nil {
		t.Fatal(err)
	}
	if rec.paths[0] == "" || rec.paths[0] != rec.paths[1] {
		t.Fatalf("a repeated start got another workspace: %v", rec.paths)
	}
	// The agent of the first attempt keeps a file open after its controller died.
	orphan, err := os.Create(filepath.Join(rec.paths[0], "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = orphan.Close() }()

	restarted := &FreshWorkspaces{Backend: rec, Root: root}
	if _, err := restarted.Start(ctx, coderTask("run-1", "run-1/attempt/2"), agent.WorkspaceRef{ID: "run-1"}, nil); err != nil {
		t.Fatal(err)
	}
	resumed := rec.paths[2]
	if resumed == rec.paths[0] {
		t.Fatal("the resumed attempt got the orphan's workspace")
	}
	if _, err := os.Stat(rec.paths[0]); !os.IsNotExist(err) {
		t.Fatalf("the orphan's workspace is still there: %v", err)
	}
	if _, err := orphan.WriteString("package main // written by the orphan\n"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(resumed)
	if err != nil || len(entries) != 0 {
		t.Fatalf("the resumed workspace holds %v (%v)", entries, err)
	}
}

func TestPiIsRefusedOutsideASandboxedWorkspace(t *testing.T) {
	t.Parallel()
	if err := SandboxedAgent("pi", false, "/w"); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("pi outside a sandbox: %v", err)
	}
	if err := SandboxedAgent("pi", true, ""); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("pi without fresh workspaces: %v", err)
	}
	if err := SandboxedAgent("pi", true, "/w"); err != nil {
		t.Fatal(err)
	}
	if err := SandboxedAgent("claude", false, ""); err != nil {
		t.Fatal(err)
	}
}

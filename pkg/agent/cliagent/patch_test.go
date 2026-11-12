package cliagent_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestTheCoderLeavesAPatchOfTrackedAndNewFilesAgainstTheBase(t *testing.T) {
	t.Parallel()
	r := newRig(t, clitest.Script{Steps: []clitest.Step{
		{WriteFile: map[string]string{"README.md": "changed\n", "version.go": "package main\n"}},
		{Emit: `{"type":"done"}`},
	}})
	base := initGitRepo(t, r.ws.Path)
	r.ws.BaseSHA = base
	ref := r.start(t, newTask("run-0001", "k1"))
	r.wait(t, ref)
	artifacts, err := r.b.CollectArtifacts(context.Background(), ref)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	var patch string
	for _, a := range artifacts {
		if a.Kind == agent.ArtifactPatch {
			data, _ := os.ReadFile(strings.TrimPrefix(a.URI, "file://"))
			patch = string(data)
			if err := a.Digest.Validate(); err != nil {
				t.Errorf("patch digest: %v", err)
			}
		}
	}
	if !strings.Contains(patch, "+changed") || !strings.Contains(patch, "b/version.go") {
		t.Errorf("patch lacks the edit or the new file:\n%s", patch)
	}
	// The workspace's own index is untouched: the new file is still untracked.
	if out := gitOut(t, r.ws.Path, "status", "--porcelain"); !strings.Contains(out, "?? version.go") {
		t.Errorf("collecting a patch changed the workspace index:\n%s", out)
	}
}

func TestAQAAgentOrANonRepositoryWorkspaceLeavesNoPatch(t *testing.T) {
	t.Parallel()
	r := newRig(t, clitest.Script{Steps: []clitest.Step{{Emit: `{"type":"done"}`}}})
	ref := r.start(t, newTask("run-0001", "k1"))
	r.wait(t, ref)
	artifacts, err := r.b.CollectArtifacts(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range artifacts {
		if a.Kind == agent.ArtifactPatch {
			t.Errorf("a workspace that is not a repository produced a patch %s", a.Name)
		}
	}
	qa := newRig(t, clitest.Script{Steps: []clitest.Step{{Emit: `{"type":"done"}`}}})
	initGitRepo(t, qa.ws.Path)
	task := newTask("run-0002", "k2")
	task.Role, task.Step = agent.RoleQA, core.StepQA
	qref := qa.start(t, task)
	qa.wait(t, qref)
	artifacts, err = qa.b.CollectArtifacts(context.Background(), qref)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range artifacts {
		if a.Kind == agent.ArtifactPatch {
			t.Errorf("an evaluator must not produce a patch, got %s", a.Name)
		}
	}
}

// An agent that rewrites the repository configuration to make git run a
// command must not get that command run when its patch is collected.
func TestAWorkspaceConfigThatMakesGitRunACommandIsRefused(t *testing.T) {
	t.Parallel()
	canary := filepath.Join(t.TempDir(), "executed")
	r := newRig(t, clitest.Script{Steps: []clitest.Step{{Emit: `{"type":"done"}`}}})
	initGitRepo(t, r.ws.Path)
	config := "\n[filter \"pwn\"]\n\tclean = touch " + canary + "\n[core]\n\tfsmonitor = touch " + canary + "\n"
	f, err := os.OpenFile(filepath.Join(r.ws.Path, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(config); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.WriteFile(filepath.Join(r.ws.Path, ".gitattributes"), []byte("* filter=pwn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := r.start(t, newTask("run-0001", "k1"))
	r.wait(t, ref)
	if _, err := r.b.CollectArtifacts(context.Background(), ref); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("want PERMISSION_DENIED, got %v", err)
	}
	if _, err := os.Stat(canary); err == nil {
		t.Fatal("collecting the patch executed a command named by the workspace")
	}
}

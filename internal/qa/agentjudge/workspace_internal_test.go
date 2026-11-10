//go:build unix

package agentjudge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

func TestTheWorkspaceIsAReadOnlyViewOfTheCandidateOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	m, err := Materials{Diff: "--- a/x\n+++ b/x\n", Files: []File{
		{Path: "app/main.go", Content: []byte("package main\n")},
		{Path: "app/main.go", Content: []byte("a second copy is dropped")},
		{Path: "big.bin", Content: make([]byte, MaxFileBytes+1)},
	}}.check()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := newWorkspace(root, m)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, workspaceFiles, "app", "main.go"))
	if err != nil || string(content) != "package main\n" {
		t.Fatalf("the candidate file reads %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(dir, workspaceFiles, "big.bin")); !os.IsNotExist(err) {
		t.Fatal("a file over the limit was copied")
	}
	if os.Geteuid() != 0 {
		if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o600); err == nil {
			t.Fatal("the workspace accepts new files")
		}
		if err := os.WriteFile(filepath.Join(dir, workspaceDiff), []byte("x"), 0o600); err == nil {
			t.Fatal("the diff can be rewritten")
		}
	}
	removeWorkspace(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the workspace was not removed: %v", err)
	}
}

func TestMaterialsThatCouldEscapeTheWorkspaceOrHaveNoDiffAreRefused(t *testing.T) {
	t.Parallel()
	for name, m := range map[string]Materials{
		"no diff":          {Diff: " \n"},
		"diff too large":   {Diff: string(make([]byte, MaxDiffBytes+1))},
		"parent directory": {Diff: "d", Files: []File{{Path: "../etc/passwd"}}},
		"absolute path":    {Diff: "d", Files: []File{{Path: "/etc/passwd"}}},
		"unclean path":     {Diff: "d", Files: []File{{Path: "app/../../x"}}},
		"backslash":        {Diff: "d", Files: []File{{Path: `..\x`}}},
		"empty path":       {Diff: "d", Files: []File{{Path: ""}}},
	} {
		if _, err := m.check(); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: %v, want INVALID_ARGUMENT", name, err)
		}
	}
}

package github_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// The tree of a commit lists its files with their kind and mode, and the
// directories above them; a listing GitHub cut short is refused.
func TestListTreeListsEveryEntryOrRefuses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake, _, client := gitDataClient(t)
	sha, err := fake.Commit(testRepo, map[string]string{"content.lock": "core wordpress 7.0.4 x\n", "plugins/pay/pay.php": "<?php", "plugins/link": "pay"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fake.SetEntryKind(testRepo, sha, "plugins/link", "symlink"); err != nil {
		t.Fatal(err)
	}
	repo := github.RepoRef{Owner: "acme", Name: "app"}
	entries, err := client.ListTree(ctx, repo, core.CommitSHA(sha))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]github.TreeEntry{}
	for _, e := range entries {
		got[e.Path] = e
	}
	if e := got["plugins/pay/pay.php"]; e.Type != github.TreeBlob || e.Mode != github.ModeFile || e.Size != 5 {
		t.Fatalf("pay.php = %+v", e)
	}
	if got["plugins/link"].Mode != github.ModeSymlink || got["plugins/pay"].Type != github.TreeDir {
		t.Fatalf("entries %+v", got)
	}
	fake.TruncateTrees()
	if _, err := client.ListTree(ctx, repo, core.CommitSHA(sha)); !core.HasCode(err, core.CodeLimitExceeded) {
		t.Fatalf("a cut listing = %v, want LIMIT_EXCEEDED", err)
	}
}

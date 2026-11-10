package github_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

func TestReadFileReturnsTheContentAtAnExactCommit(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	content := strings.Repeat("policy line\n", 400)
	sha, err := f.fake.Commit(testRepo, map[string]string{".sisf/factory.yaml": content, "AGENTS.md": "rules"})
	if err != nil {
		t.Fatal(err)
	}
	repo := github.RepoRef{Owner: "acme", Name: "app"}
	got, err := f.client.ReadFile(context.Background(), repo, core.CommitSHA(sha), ".sisf/factory.yaml")
	if err != nil || string(got) != content {
		t.Fatalf("ReadFile = %d bytes, %v", len(got), err)
	}
	_, err = f.client.ReadFile(context.Background(), repo, core.CommitSHA(sha), "REVIEW.md")
	if !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("a file the commit lacks: %v", err)
	}
}

func TestReadFileRefusesMovingRefsAndEscapingPathsBeforeAnyRequest(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	repo := github.RepoRef{Owner: "acme", Name: "app"}
	const sha core.CommitSHA = "0123456789abcdef0123456789abcdef01234567"
	cases := map[string]struct {
		revision core.CommitSHA
		path     string
	}{
		"a branch":          {"main", "AGENTS.md"},
		"a short sha":       {"0123456", "AGENTS.md"},
		"a parent segment":  {sha, "../secrets"},
		"an absolute path":  {sha, "/etc/passwd"},
		"an empty segment":  {sha, "a//b"},
		"no path":           {sha, ""},
		"a backslash":       {sha, `a\b`},
		"a current segment": {sha, "./AGENTS.md"},
	}
	for name, c := range cases {
		if _, err := f.client.ReadFile(context.Background(), repo, c.revision, c.path); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: want INVALID_ARGUMENT, got %v", name, err)
		}
	}
	if n := len(f.fake.Requests()); n != 0 {
		t.Fatalf("%d requests were made for refused reads", n)
	}
}

package github_test

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// landingRepo is a repository with main and one candidate branch on it,
// and the candidate's pull request.
func landingRepo(t *testing.T, mainFiles map[string]string, change map[string][]byte) (*fakeLanding, github.PullRequest) {
	t.Helper()
	fake, data, client := gitDataClient(t)
	base, err := fake.Commit(testRepo, mainFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.SetBranch(testRepo, "main", base); err != nil {
		t.Fatal(err)
	}
	head, err := client.CommitToBranch(bg, repoRef, github.BranchCommit{Branch: "sisf/f/run-1-1", Base: core.CommitSHA(base),
		Message: "candidate", Author: "service:builder", Files: change})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := client.CreatePullRequest(bg, repoRef, github.NewPullRequest{Title: "Greet", Body: "Refs #1", HeadRef: "sisf/f/run-1-1", BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	merger, err := github.NewMerger(client)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeLanding{merges: data.Merges, branch: data.Branch, client: client, merger: merger, head: head, base: core.CommitSHA(base)}, pr
}

type fakeLanding struct {
	merges func() int
	branch func(string, string) (string, map[string]string, bool)
	client *github.RESTClient
	merger *github.Merger
	head   core.CommitSHA
	base   core.CommitSHA
}

// A candidate's pull request is found by its branch, with the head the
// branch has, and merged at that head onto main.
func TestAPullRequestIsFoundByItsBranchAndMergedAtItsHead(t *testing.T) {
	t.Parallel()
	l, pr := landingRepo(t, map[string]string{"app/main.go": "old\n", "README.md": "r\n"}, map[string][]byte{"app/main.go": []byte("new\n")})
	found, err := l.client.FindPullRequests(bg, repoRef, "sisf/f/run-1-1")
	if err != nil || len(found) != 1 || found[0].Number != pr.Number || found[0].HeadSHA != l.head || found[0].BaseRef != "main" {
		t.Fatalf("FindPullRequests = %+v, %v", found, err)
	}
	merged, err := l.merger.Merge(bg, repoRef, github.MergeRequest{Number: pr.Number, HeadSHA: l.head, Title: "Merge"})
	if err != nil {
		t.Fatal(err)
	}
	sha, files, _ := l.branch(testRepo, "main")
	if sha != string(merged.SHA) || files["app/main.go"] != "new\n" || files["README.md"] != "r\n" || l.merges() != 1 {
		t.Fatalf("main is %s with %v after the merge %s", sha, files, merged.SHA)
	}
	found, err = l.client.FindPullRequests(bg, repoRef, "sisf/f/run-1-1")
	if err != nil || len(found) != 1 || !found[0].Merged || found[0].State != github.IssueClosed || found[0].HeadSHA != l.head {
		t.Fatalf("after the merge the pull request reads %+v, %v", found, err)
	}
	if _, err := l.merger.Merge(bg, repoRef, github.MergeRequest{Number: pr.Number, HeadSHA: l.head, Title: "Merge"}); github.ErrorStatus(err) != 405 {
		t.Fatalf("a merged pull request merged again: %v", err)
	}
}

// GitHub's guard: a merge at a head the branch no longer has is refused
// with CONFLICT, and main does not move.
func TestAMergeAtAHeadTheBranchMovedOffIsRefused(t *testing.T) {
	t.Parallel()
	l, pr := landingRepo(t, map[string]string{"app/main.go": "old\n"}, map[string][]byte{"app/main.go": []byte("new\n")})
	if _, err := l.client.CommitToBranch(bg, repoRef, github.BranchCommit{Branch: "sisf/f/run-1-1", Base: l.head, Message: "more",
		Author: "someone", Files: map[string][]byte{"app/other.go": []byte("x\n")}}); err != nil {
		t.Fatal(err)
	}
	_, err := l.merger.Merge(bg, repoRef, github.MergeRequest{Number: pr.Number, HeadSHA: l.head, Title: "Merge"})
	if !core.HasCode(err, core.CodeConflict) {
		t.Fatalf("a merge at a stale head answered %v", err)
	}
	if sha, _, _ := l.branch(testRepo, "main"); core.CommitSHA(sha) != l.base || l.merges() != 0 {
		t.Fatalf("main moved to %s", sha)
	}
}

// The token's own account is read from GitHub.
func TestTheViewerIsTheTokensAccount(t *testing.T) {
	t.Parallel()
	fake, _, client := gitDataClient(t)
	u, err := client.Viewer(bg)
	if err != nil || u.Login != fake.Bot().Login || u.ID != fake.Bot().ID {
		t.Fatalf("Viewer = %+v, %v", u, err)
	}
}

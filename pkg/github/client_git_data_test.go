package github_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// gitDataClient serves the fake with its git data routes and returns a
// client for it.
func gitDataClient(t *testing.T) (*fakegithub.Server, *fakegithub.GitData, *github.RESTClient) {
	t.Helper()
	fake := fakegithub.New(fakegithub.WithToken(testToken))
	if err := fake.AddRepo(testRepo); err != nil {
		t.Fatal(err)
	}
	data := fakegithub.NewGitData(fake)
	srv := httptest.NewServer(data)
	t.Cleanup(srv.Close)
	client, err := github.NewRESTClient(github.ClientConfig{BaseURL: srv.URL,
		Token: github.StaticToken(core.NewSecretValue([]byte(testToken))), Clock: newFakeClock()})
	if err != nil {
		t.Fatal(err)
	}
	return fake, data, client
}

// A commit on a new branch keeps the base's files, changes only the given
// ones, leaves the base branch where it was, and shows in the compare API.
func TestCommitToBranchCarriesTheBaseForwardAndLeavesTheBaseBranch(t *testing.T) {
	t.Parallel()
	fake, data, client := gitDataClient(t)
	base, err := fake.Commit(testRepo, map[string]string{"README.md": "hello\n", "go.mod": "module x\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := data.SetBranch(testRepo, "main", base); err != nil {
		t.Fatal(err)
	}
	head, err := client.BranchHead(bg, repoRef, "main")
	if err != nil || string(head) != base {
		t.Fatalf("BranchHead(main) = %s, %v", head, err)
	}
	sha, err := client.CommitToBranch(bg, repoRef, github.BranchCommit{
		Branch: "sisf/factory-a/run-1", Base: head, Message: "change the readme", Author: "service:publisher",
		Files: map[string][]byte{"README.md": []byte("hello, world\n"), "docs/new.md": []byte("new\n")},
	})
	if err != nil {
		t.Fatalf("CommitToBranch: %v", err)
	}
	_, files, ok := data.Branch(testRepo, "sisf/factory-a/run-1")
	if !ok || files["README.md"] != "hello, world\n" || files["go.mod"] != "module x\n" || files["docs/new.md"] != "new\n" {
		t.Fatalf("the branch holds %v", files)
	}
	if got, _, _ := data.Branch(testRepo, "main"); got != base {
		t.Fatalf("main moved to %s", got)
	}
	read, err := client.ReadFile(bg, repoRef, sha, "README.md")
	if err != nil || string(read) != "hello, world\n" {
		t.Fatalf("the contents API reads %q, %v", read, err)
	}
	paths, err := client.ChangedPaths(bg, repoRef, head, sha)
	if err != nil || !slices.Equal(paths, []string{"README.md", "docs/new.md"}) {
		t.Fatalf("ChangedPaths = %v, %v", paths, err)
	}
	// Writing the branch again moves it; nothing else changes.
	again, err := client.CommitToBranch(bg, repoRef, github.BranchCommit{
		Branch: "sisf/factory-a/run-1", Base: head, Message: "again", Files: map[string][]byte{"README.md": nil},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, files, _ := data.Branch(testRepo, "sisf/factory-a/run-1"); got != string(again) || files["README.md"] != "" {
		t.Fatalf("the branch was not moved to the commit that deletes the readme: %s %v", got, files)
	}
}

func TestBranchNamesThatCouldLeaveTheirRefAreRefused(t *testing.T) {
	t.Parallel()
	_, _, client := gitDataClient(t)
	for _, name := range []string{"../main", "a//b", "a/", "-x", "a.lock", "a..b"} {
		if _, err := client.BranchHead(bg, repoRef, name); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("BranchHead(%q) = %v, want INVALID_ARGUMENT", name, err)
		}
	}
	if _, err := client.BranchHead(bg, repoRef, "missing"); !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("a missing branch: %v", err)
	}
}

// The factory writes only its own branches: no commit through the client
// can move the default branch or any branch a person owns.
func TestCommitToBranchWritesOnlyTheFactorysOwnBranches(t *testing.T) {
	t.Parallel()
	fake, data, client := gitDataClient(t)
	base, err := fake.Commit(testRepo, map[string]string{"README.md": "hello\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := data.SetBranch(testRepo, "main", base); err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"main", "release/1.0", "feature/sisf/x"} {
		_, err := client.CommitToBranch(bg, repoRef, github.BranchCommit{Branch: branch, Base: core.CommitSHA(base), Message: "m",
			Files: map[string][]byte{"README.md": []byte("changed\n")}})
		if !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("CommitToBranch(%s) = %v", branch, err)
		}
	}
	if head, _, _ := data.Branch(testRepo, "main"); head != base || data.Writes() != 0 {
		t.Fatalf("main moved to %s after %d writes", head, data.Writes())
	}
}

// GitHub's comparison lists at most CompareFileLimit files. A listing that
// long may leave files out, so it is answered with LIMIT_EXCEEDED and a
// caller that must see every changed path fails closed.
func TestAComparisonAtGitHubsFileLimitIsReportedAsPossiblyIncomplete(t *testing.T) {
	t.Parallel()
	fake, data, client := gitDataClient(t)
	base, err := fake.Commit(testRepo, map[string]string{"README.md": "hello\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := data.SetBranch(testRepo, "main", base); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{github.CompareFileLimit - 1, github.CompareFileLimit} {
		head := core.CommitSHA(base)
		for from := 0; from < n; from += 50 {
			files := map[string][]byte{}
			for i := from; i < min(from+50, n); i++ {
				files[fmt.Sprintf("app/f%03d.txt", i)] = []byte("x\n")
			}
			if head, err = client.CommitToBranch(bg, repoRef, github.BranchCommit{Branch: fmt.Sprintf("sisf/f/run-%d", n), Base: head,
				Message: "many files", Files: files}); err != nil {
				t.Fatal(err)
			}
		}
		paths, err := client.ChangedPaths(bg, repoRef, core.CommitSHA(base), head)
		if limited := core.HasCode(err, core.CodeLimitExceeded); limited != (n >= github.CompareFileLimit) || len(paths) != n {
			t.Fatalf("%d changed files: %d listed, %v", n, len(paths), err)
		}
	}
}

// GitHub's comparison reports a renamed or copied file as one entry: its
// new path as filename and its old one as previous_filename. Both are
// changed paths, so a file moved out of a protected directory is listed
// under the path it left as well as the one it reached. Only the entries
// GitHub lists count toward its limit, not the paths they name.
func TestChangedPathsNamesTheOldPathOfARenameOrCopy(t *testing.T) {
	t.Parallel()
	const (
		base = core.CommitSHA("1111111111111111111111111111111111111111")
		head = core.CommitSHA("2222222222222222222222222222222222222222")
	)
	files := `[
		{"sha":"bbcd538c8e72b8c175046e27cc8f907076331401","filename":"examples/hello-app/approval.html","previous_filename":"ui/templates/approval.html","status":"renamed","additions":0,"deletions":0,"changes":0},
		{"sha":"bbcd538c8e72b8c175046e27cc8f907076331401","filename":"examples/hello-app/copy.html","previous_filename":"internal/qa/thresholds.txt","status":"copied","additions":0,"deletions":0,"changes":0},
		{"sha":"bbcd538c8e72b8c175046e27cc8f907076331401","filename":"examples/hello-app/main.go","status":"modified","additions":1,"deletions":1,"changes":2}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/app/compare/"+string(base)+"..."+string(head) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"ahead","ahead_by":1,"behind_by":0,"total_commits":1,"files":%s}`, files)
	}))
	t.Cleanup(srv.Close)
	client, err := github.NewRESTClient(github.ClientConfig{BaseURL: srv.URL,
		Token: github.StaticToken(core.NewSecretValue([]byte(testToken))), Clock: newFakeClock()})
	if err != nil {
		t.Fatal(err)
	}
	paths, err := client.ChangedPaths(bg, repoRef, base, head)
	want := []string{"examples/hello-app/approval.html", "examples/hello-app/copy.html", "examples/hello-app/main.go",
		"internal/qa/thresholds.txt", "ui/templates/approval.html"}
	if err != nil || !slices.Equal(paths, want) {
		t.Fatalf("ChangedPaths = %v, %v; want %v", paths, err, want)
	}
}

// A comparison of renames below GitHub's limit of entries is complete,
// though it names twice as many paths.
func TestRenamesBelowTheLimitAreNotTakenForAListingCutShort(t *testing.T) {
	t.Parallel()
	fake, data, client := gitDataClient(t)
	before := map[string]string{}
	for i := range github.CompareFileLimit / 2 {
		before[fmt.Sprintf("ui/templates/%03d.html", i)] = fmt.Sprintf("page %d\n", i)
	}
	base, err := fake.Commit(testRepo, before)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.SetBranch(testRepo, "main", base); err != nil {
		t.Fatal(err)
	}
	moved := map[string][]byte{}
	for path, content := range before {
		moved[path] = nil
		moved["examples/hello-app/"+path] = []byte(content)
	}
	head, err := client.CommitToBranch(bg, repoRef, github.BranchCommit{Branch: "sisf/factory-a/run-9", Base: core.CommitSHA(base),
		Message: "move", Files: moved})
	if err != nil {
		t.Fatal(err)
	}
	paths, err := client.ChangedPaths(bg, repoRef, core.CommitSHA(base), head)
	if err != nil || len(paths) != 2*len(before) {
		t.Fatalf("ChangedPaths listed %d paths, %v; want %d and no limit", len(paths), err, 2*len(before))
	}
}

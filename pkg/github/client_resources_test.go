package github_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

func TestIssueRoundTripThroughTheClient(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	created, err := f.client.CreateIssue(bg, repoRef, github.NewIssue{Title: "Crash", Body: "stack", Labels: []string{github.LabelKindBug}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Ref != repoRef.Issue(1) || created.State != github.IssueOpen || !created.HasLabel(github.LabelKindBug) || !created.Author.Bot {
		t.Fatalf("created = %+v", created)
	}
	if err := f.client.AddLabels(bg, created.Ref, github.LabelReady, github.LabelKindBug); err != nil {
		t.Fatal(err)
	}
	if err := f.client.RemoveLabel(bg, created.Ref, github.LabelKindBug); err != nil {
		t.Fatal(err)
	}
	got, err := f.client.GetIssue(bg, created.Ref)
	if err != nil || len(got.Labels) != 1 || got.Labels[0].Name != github.LabelReady {
		t.Fatalf("labels after the edits = %+v, %v", got.Labels, err)
	}
	if err := f.client.SetIssueState(bg, created.Ref, github.IssueClosed); err != nil {
		t.Fatal(err)
	}
	if got, _ = f.client.GetIssue(bg, created.Ref); got.State != github.IssueClosed {
		t.Fatalf("state = %q", got.State)
	}
	if err := f.client.AddLabels(bg, created.Ref); err != nil {
		t.Fatalf("adding no labels is a no-op: %v", err)
	}
}

func TestCommentsRoundTripAndFilterBySince(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	n := mustOpen(t, f, f.person("alice"), "idea")
	ref := repoRef.Issue(n)
	first, err := f.client.CreateComment(bg, ref, "one")
	if err != nil || first.Issue != ref || first.Body != "one" || !first.Author.Bot {
		t.Fatalf("created = %+v, %v", first, err)
	}
	second, _ := f.client.CreateComment(bg, ref, "two")
	updated, err := f.client.UpdateComment(bg, repoRef, first.ID, "one, edited")
	if err != nil || updated.Body != "one, edited" {
		t.Fatalf("updated = %+v, %v", updated, err)
	}
	all, err := f.client.ListComments(bg, ref, time.Time{})
	if err != nil || len(all) != 2 {
		t.Fatalf("comments = %+v, %v", all, err)
	}
	recent, _ := f.client.ListComments(bg, ref, updated.UpdatedAt)
	if len(recent) != 1 || recent[0].ID != first.ID {
		t.Fatalf("comments since the edit = %+v (second is %d)", recent, second.ID)
	}
}

func TestPullRequestsChecksAndReleases(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	pr, err := f.client.CreatePullRequest(bg, repoRef, github.NewPullRequest{Title: "Fix", HeadRef: "sisf/fix", BaseRef: "main", Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 1 || pr.IsFork || !pr.Draft || pr.HeadRef != "sisf/fix" || pr.HeadSHA.Validate() != nil {
		t.Fatalf("pull request = %+v", pr)
	}
	read, err := f.client.GetPullRequest(bg, repoRef, pr.Number)
	if err != nil || read.HeadSHA != pr.HeadSHA {
		t.Fatalf("read = %+v, %v", read, err)
	}
	started, err := f.client.PutCheckRun(bg, repoRef, github.CheckRun{Name: "sisf/qa", HeadSHA: pr.HeadSHA, Status: github.CheckInProgress})
	if err != nil || started.ID == 0 {
		t.Fatalf("started = %+v, %v", started, err)
	}
	started.Status, started.Conclusion, started.Summary = github.CheckCompleted, github.ConclusionSuccess, "all gates passed"
	done, err := f.client.PutCheckRun(bg, repoRef, started)
	if err != nil || done.ID != started.ID || done.Conclusion != github.ConclusionSuccess {
		t.Fatalf("completed = %+v, %v", done, err)
	}
	if runs, _ := f.fake.CheckRuns(testRepo); len(runs) != 1 || runs[0] != "sisf/qa:success" {
		t.Fatalf("check runs on the server = %v", runs)
	}
	rel, err := f.client.CreateRelease(bg, repoRef, github.Release{Tag: "v0.1.0", Name: "first"})
	if err != nil || rel.Tag != "v0.1.0" {
		t.Fatalf("release = %+v, %v", rel, err)
	}
	if _, err := f.client.CreateRelease(bg, repoRef, github.Release{Tag: "v0.1.0"}); !core.HasCode(err, core.CodeAlreadyExists) {
		t.Fatalf("a second release of a tag: %v, want ALREADY_EXISTS", err)
	}
}

func TestFailuresAreTypedAndNeverClaimSuccess(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	missing := repoRef.Issue(99)
	checks := map[string]struct {
		err  error
		want core.ErrorCode
	}{
		"missing issue":      {second(f.client.GetIssue(bg, missing)), core.CodeNotFound},
		"missing pull":       {second(f.client.GetPullRequest(bg, repoRef, 99)), core.CodeNotFound},
		"missing repo":       {second(f.client.ListIssues(bg, github.RepoRef{Owner: "acme", Name: "nope"}, github.ListIssuesOptions{})), core.CodeNotFound},
		"label of nothing":   {f.client.RemoveLabel(bg, missing, "x"), core.CodeNotFound},
		"close nothing":      {f.client.SetIssueState(bg, missing, github.IssueClosed), core.CodeNotFound},
		"comment on nothing": {second(f.client.CreateComment(bg, missing, "hi")), core.CodeNotFound},
	}
	for name, c := range checks {
		if !core.HasCode(c.err, c.want) {
			t.Errorf("%s: %v, want %s", name, c.err, c.want)
		}
	}
}

func second[T any](_ T, err error) error { return err }

func TestInvalidInputIsRejectedBeforeAnyRequest(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	ref := repoRef.Issue(1)
	sha := core.CommitSHA(strings.Repeat("a", 40))
	checks := map[string]error{
		"zero issue ref":              second(f.client.GetIssue(bg, core.IssueRef{})),
		"path in owner":               second(f.client.ListIssues(bg, github.RepoRef{Owner: "acme/../x", Name: "app"}, github.ListIssuesOptions{})),
		"dot-dot repository":          second(f.client.ListIssues(bg, github.RepoRef{Owner: "acme", Name: ".."}, github.ListIssuesOptions{})),
		"bad state filter":            second(f.client.ListIssues(bg, repoRef, github.ListIssuesOptions{State: "merged"})),
		"untitled issue":              second(f.client.CreateIssue(bg, repoRef, github.NewIssue{Title: "  "})),
		"bad state":                   f.client.SetIssueState(bg, ref, "merged"),
		"empty label":                 f.client.RemoveLabel(bg, ref, ""),
		"empty comment":               second(f.client.CreateComment(bg, ref, "")),
		"oversized comment":           second(f.client.CreateComment(bg, ref, strings.Repeat("x", 65537))),
		"update without id":           second(f.client.UpdateComment(bg, repoRef, 0, "x")),
		"pull without head":           second(f.client.CreatePullRequest(bg, repoRef, github.NewPullRequest{Title: "x", BaseRef: "main"})),
		"pull number zero":            second(f.client.GetPullRequest(bg, repoRef, 0)),
		"check without name":          second(f.client.PutCheckRun(bg, repoRef, github.CheckRun{HeadSHA: sha, Status: github.CheckInProgress})),
		"completed without result":    second(f.client.PutCheckRun(bg, repoRef, github.CheckRun{Name: "q", HeadSHA: sha, Status: github.CheckCompleted})),
		"check on an abbreviated sha": second(f.client.PutCheckRun(bg, repoRef, github.CheckRun{Name: "q", HeadSHA: "abc123", Status: github.CheckInProgress})),
		"release without tag":         second(f.client.CreateRelease(bg, repoRef, github.Release{})),
		"release on a branch name":    second(f.client.CreateRelease(bg, repoRef, github.Release{Tag: "v1", TargetSHA: "main"})),
	}
	for name, err := range checks {
		if !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: %v, want INVALID_ARGUMENT", name, err)
		}
	}
	if n := len(f.fake.Requests()); n != 0 {
		t.Fatalf("%d requests were sent for input that was invalid", n)
	}
}

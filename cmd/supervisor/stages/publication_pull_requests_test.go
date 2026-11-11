package stages

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/github"
)

// fakePulls opens pull requests and finds them again by their branch.
type fakePulls struct {
	opened []github.PullRequest
}

func (f *fakePulls) FindPullRequests(_ context.Context, _ github.RepoRef, branch string) ([]github.PullRequest, error) {
	var out []github.PullRequest
	for _, pr := range f.opened {
		if pr.HeadRef == branch {
			out = append(out, pr)
		}
	}
	return out, nil
}

func (f *fakePulls) CreatePullRequest(_ context.Context, repo github.RepoRef, pr github.NewPullRequest) (github.PullRequest, error) {
	opened := github.PullRequest{Repo: repo, Number: len(f.opened) + 1, State: github.IssueOpen, HeadRef: pr.HeadRef, BaseRef: pr.BaseRef}
	f.opened = append(f.opened, opened)
	return opened, nil
}

// Publishing a verified release again, after its pull request was opened
// but before that was recorded, finds the open pull request instead of
// opening a second one; a closed one is not reused.
func TestAPublicationPullRequestIsOpenedOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := github.RepoRef{Owner: "acme", Name: "hello"}
	fake := &fakePulls{}
	pulls := PublicationPulls{GitHub: fake}
	req := github.NewPullRequest{Title: "Record", HeadRef: "sisf/release/run-1", BaseRef: "main"}
	first, err := pulls.CreatePullRequest(ctx, repo, req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := pulls.CreatePullRequest(ctx, repo, req)
	if err != nil || again.Number != first.Number || len(fake.opened) != 1 {
		t.Fatalf("the second publication = #%d, %v with %d opened; want #%d and one opened", again.Number, err, len(fake.opened), first.Number)
	}
	fake.opened[0].State = github.IssueClosed
	if third, err := pulls.CreatePullRequest(ctx, repo, req); err != nil || third.Number == first.Number {
		t.Fatalf("after the first was closed = #%d, %v; want a new pull request", third.Number, err)
	}
}

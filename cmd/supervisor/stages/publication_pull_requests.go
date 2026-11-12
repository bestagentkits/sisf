package stages

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/github"
)

// PublicationPullsGitHub is what PublicationPulls uses of GitHub.
type PublicationPullsGitHub interface {
	FindPullRequests(ctx context.Context, repo github.RepoRef, branch string) ([]github.PullRequest, error)
	CreatePullRequest(ctx context.Context, repo github.RepoRef, pr github.NewPullRequest) (github.PullRequest, error)
}

// PublicationPulls opens the Publisher's pull request of a verified
// release once. Publishing is repeated when a supervisor stopped, or a
// later step failed, after the pull request was opened but before that was
// recorded; the open pull request of the release's branch into the same
// base is then answered instead of asking GitHub to open a second one,
// which it would refuse.
type PublicationPulls struct{ GitHub PublicationPullsGitHub }

// CreatePullRequest answers the open pull request from pr.HeadRef into
// pr.BaseRef, or opens one.
func (p PublicationPulls) CreatePullRequest(ctx context.Context, repo github.RepoRef, pr github.NewPullRequest) (github.PullRequest, error) {
	found, err := p.GitHub.FindPullRequests(ctx, repo, pr.HeadRef)
	if err != nil {
		return github.PullRequest{}, err
	}
	for _, existing := range found {
		if existing.State == github.IssueOpen && !existing.Merged && existing.BaseRef == pr.BaseRef {
			return existing, nil
		}
	}
	return p.GitHub.CreatePullRequest(ctx, repo, pr)
}

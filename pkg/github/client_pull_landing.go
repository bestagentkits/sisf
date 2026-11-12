package github

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// FindPullRequests lists the pull requests, open or closed, whose head is
// branch of repo itself. A candidate's pull request is found again by its
// branch after a restart, whether or not it was merged in the meantime.
func (c *RESTClient) FindPullRequests(ctx context.Context, repo RepoRef, branch string) ([]PullRequest, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if strings.TrimSpace(branch) == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "a branch is required to find its pull requests")
	}
	q := url.Values{"state": {"all"}, "head": {repo.Owner + ":" + branch}, "per_page": {"100"}}
	var out []PullRequest
	_, err := c.getPages(ctx, repoPath(repo, "pulls"), q, nil, func(resp apiResponse) error {
		var wire []wirePull
		if err := decode(resp, &wire); err != nil {
			return err
		}
		for _, w := range wire {
			if pr := w.pull(repo); pr.HeadRef == branch && !pr.IsFork {
				out = append(out, pr)
			}
		}
		return nil
	})
	return out, err
}

// Viewer returns the account the client's token acts as.
func (c *RESTClient) Viewer(ctx context.Context) (User, error) {
	resp, err := c.call(ctx, http.MethodGet, "/user", nil, nil, nil)
	if err != nil {
		return User{}, err
	}
	var w wireUser
	if err := decode(resp, &w); err != nil {
		return User{}, err
	}
	if w.ID == 0 && w.Login == "" {
		return User{}, core.NewError(core.CodeUnavailable, "GitHub named no account for the token")
	}
	return w.user(), nil
}

// MergeRequest asks to merge one pull request exactly at HeadSHA.
type MergeRequest struct {
	Number  int
	HeadSHA core.CommitSHA
	Title   string
	Message string
}

// Merged is a merge GitHub made.
type Merged struct {
	SHA core.CommitSHA
}

// Merger merges pull requests into their base branch. It is the factory's
// only way to change a default branch, it is not part of Client, and only
// the supervisor builds one: the agent and control zones are handed clients
// that cannot merge.
type Merger struct{ client *RESTClient }

// NewMerger returns a merger acting with client's token.
func NewMerger(client *RESTClient) (*Merger, error) {
	if client == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "a merger needs a GitHub client")
	}
	return &Merger{client: client}, nil
}

// Merge merges the pull request with a merge commit, only while its head is
// still req.HeadSHA: GitHub refuses with CONFLICT when the branch moved. A
// pull request GitHub will not merge (a conflict, a protection rule, a
// required check) is refused with its status, which ErrorStatus reports.
func (m *Merger) Merge(ctx context.Context, repo RepoRef, req MergeRequest) (Merged, error) {
	if err := validateRepo(repo); err != nil {
		return Merged{}, err
	}
	if req.Number < 1 || req.HeadSHA.Validate() != nil || strings.TrimSpace(req.Title) == "" {
		return Merged{}, core.NewError(core.CodeInvalidArgument, "a merge needs a pull request, the exact head commit and a title")
	}
	resp, err := m.client.call(ctx, http.MethodPut, repoPath(repo, "pulls", strconv.Itoa(req.Number), "merge"), nil, map[string]any{
		"sha": string(req.HeadSHA), "merge_method": "merge", "commit_title": req.Title, "commit_message": req.Message,
	}, nil)
	if err != nil {
		return Merged{}, err
	}
	var w struct {
		SHA    string `json:"sha"`
		Merged bool   `json:"merged"`
	}
	if err := decode(resp, &w); err != nil {
		return Merged{}, err
	}
	if !w.Merged || core.CommitSHA(w.SHA).Validate() != nil {
		return Merged{}, core.NewError(core.CodeUnavailable, "GitHub did not confirm the merge")
	}
	return Merged{SHA: core.CommitSHA(w.SHA)}, nil
}

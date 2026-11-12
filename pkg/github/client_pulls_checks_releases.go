package github

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// GetPullRequest reads one pull request.
func (c *RESTClient) GetPullRequest(ctx context.Context, repo RepoRef, number int) (PullRequest, error) {
	if err := validateRepo(repo); err != nil {
		return PullRequest{}, err
	}
	if number < 1 {
		return PullRequest{}, core.NewError(core.CodeInvalidArgument, "a pull request number is required")
	}
	resp, err := c.call(ctx, http.MethodGet, repoPath(repo, "pulls", strconv.Itoa(number)), nil, nil, nil)
	if err != nil {
		return PullRequest{}, err
	}
	var w wirePull
	if err := decode(resp, &w); err != nil {
		return PullRequest{}, err
	}
	return w.pull(repo), nil
}

// CreatePullRequest opens a pull request.
func (c *RESTClient) CreatePullRequest(ctx context.Context, repo RepoRef, pr NewPullRequest) (PullRequest, error) {
	if err := validateRepo(repo); err != nil {
		return PullRequest{}, err
	}
	if strings.TrimSpace(pr.Title) == "" || pr.HeadRef == "" || pr.BaseRef == "" {
		return PullRequest{}, core.NewError(core.CodeInvalidArgument, "a pull request needs a title, a head and a base")
	}
	resp, err := c.call(ctx, http.MethodPost, repoPath(repo, "pulls"), nil,
		map[string]any{"title": pr.Title, "body": pr.Body, "head": pr.HeadRef, "base": pr.BaseRef, "draft": pr.Draft}, nil)
	if err != nil {
		return PullRequest{}, err
	}
	var w wirePull
	if err := decode(resp, &w); err != nil {
		return PullRequest{}, err
	}
	return w.pull(repo), nil
}

type wireCheck struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	DetailsURL string `json:"details_url"`
	Output     struct {
		Summary string `json:"summary"`
	} `json:"output"`
}

// PutCheckRun creates the check run, or updates it when ID is set. A
// completed check must say how it concluded.
func (c *RESTClient) PutCheckRun(ctx context.Context, repo RepoRef, check CheckRun) (CheckRun, error) {
	if err := validateRepo(repo); err != nil {
		return CheckRun{}, err
	}
	if check.Name == "" || (check.Status != CheckInProgress && check.Status != CheckCompleted) {
		return CheckRun{}, core.NewError(core.CodeInvalidArgument, "a check run needs a name and a status of in_progress or completed")
	}
	if check.Status == CheckCompleted && check.Conclusion == "" {
		return CheckRun{}, core.NewError(core.CodeInvalidArgument, "a completed check run needs a conclusion")
	}
	body := map[string]any{"status": check.Status, "output": map[string]string{"title": check.Name, "summary": check.Summary}}
	if check.Conclusion != "" {
		body["conclusion"] = check.Conclusion
	}
	if check.DetailsURL != "" {
		body["details_url"] = check.DetailsURL
	}
	method, path := http.MethodPatch, repoPath(repo, "check-runs", strconv.FormatInt(check.ID, 10))
	if check.ID == 0 {
		if err := check.HeadSHA.Validate(); err != nil {
			return CheckRun{}, err
		}
		body["name"], body["head_sha"] = check.Name, string(check.HeadSHA)
		method, path = http.MethodPost, repoPath(repo, "check-runs")
	}
	resp, err := c.call(ctx, method, path, nil, body, nil)
	if err != nil {
		return CheckRun{}, err
	}
	var w wireCheck
	if err := decode(resp, &w); err != nil {
		return CheckRun{}, err
	}
	return CheckRun{ID: w.ID, Name: w.Name, HeadSHA: core.CommitSHA(w.HeadSHA), Status: w.Status,
		Conclusion: w.Conclusion, Summary: w.Output.Summary, DetailsURL: w.DetailsURL}, nil
}

// CreateRelease publishes a release. A tag that already has one is
// ALREADY_EXISTS.
func (c *RESTClient) CreateRelease(ctx context.Context, repo RepoRef, rel Release) (Release, error) {
	if err := validateRepo(repo); err != nil {
		return Release{}, err
	}
	if rel.Tag == "" {
		return Release{}, core.NewError(core.CodeInvalidArgument, "a release needs a tag")
	}
	body := map[string]any{"tag_name": rel.Tag, "name": rel.Name, "body": rel.Body}
	if rel.TargetSHA != "" {
		if err := rel.TargetSHA.Validate(); err != nil {
			return Release{}, err
		}
		body["target_commitish"] = string(rel.TargetSHA)
	}
	resp, err := c.call(ctx, http.MethodPost, repoPath(repo, "releases"), nil, body, nil)
	if err != nil {
		return Release{}, err
	}
	var w struct {
		Tag    string `json:"tag_name"`
		Name   string `json:"name"`
		Body   string `json:"body"`
		Target string `json:"target_commitish"`
	}
	if err := decode(resp, &w); err != nil {
		return Release{}, err
	}
	out := Release{Tag: w.Tag, Name: w.Name, Body: w.Body}
	// GitHub reports a branch name when the release targets a branch.
	if sha := core.CommitSHA(w.Target); sha.Validate() == nil {
		out.TargetSHA = sha
	}
	return out, nil
}

package github

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

func validateRef(ref core.IssueRef) error { return ref.Validate() }

func refRepo(ref core.IssueRef) RepoRef { return RepoRef{Owner: ref.Owner, Name: ref.Repo} }

// GetIssue reads one issue.
func (c *RESTClient) GetIssue(ctx context.Context, ref core.IssueRef) (Issue, error) {
	if err := validateRef(ref); err != nil {
		return Issue{}, err
	}
	resp, err := c.call(ctx, http.MethodGet, repoPath(refRepo(ref), "issues", strconv.Itoa(ref.Number)), nil, nil, nil)
	if err != nil {
		return Issue{}, err
	}
	var w wireIssue
	if err := decode(resp, &w); err != nil {
		return Issue{}, err
	}
	return w.issue(refRepo(ref)), nil
}

// IssuePage is the result of a conditional listing.
type IssuePage struct {
	Issues []Issue
	// ETag identifies this listing; send it back to learn cheaply whether
	// anything changed.
	ETag string
	// NotModified is true when GitHub answered 304 to the ETag sent.
	NotModified bool
}

// ListIssues reads every issue matching opts, oldest update first. Pull
// requests, which GitHub also lists as issues, are left out.
func (c *RESTClient) ListIssues(ctx context.Context, repo RepoRef, opts ListIssuesOptions) ([]Issue, error) {
	page, err := c.PollIssues(ctx, repo, opts, "")
	return page.Issues, err
}

// PollIssues is ListIssues with a conditional request. The listing is sorted
// by update time, newest first, which is what makes the ETag of the first
// page a valid change detector: any change to a matching issue moves it to
// the front of the first page.
func (c *RESTClient) PollIssues(ctx context.Context, repo RepoRef, opts ListIssuesOptions, etag string) (IssuePage, error) {
	if err := validateRepo(repo); err != nil {
		return IssuePage{}, err
	}
	state := string(opts.State)
	if state == "" {
		state = "all"
	}
	if state != "all" && opts.State != IssueOpen && opts.State != IssueClosed {
		return IssuePage{}, core.NewError(core.CodeInvalidArgument, "issue state must be open, closed or empty")
	}
	q := url.Values{"state": {state}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"100"}}
	if len(opts.Labels) > 0 {
		q.Set("labels", strings.Join(opts.Labels, ","))
	}
	if !opts.Since.IsZero() {
		q.Set("since", opts.Since.UTC().Format(time.RFC3339))
	}
	var header http.Header
	if etag != "" {
		header = http.Header{"If-None-Match": {etag}}
	}
	var out IssuePage
	first, err := c.getPages(ctx, repoPath(repo, "issues"), q, header, func(resp apiResponse) error {
		var wire []wireIssue
		if err := decode(resp, &wire); err != nil {
			return err
		}
		for _, w := range wire {
			if !w.isPullRequest() {
				out.Issues = append(out.Issues, w.issue(repo))
			}
		}
		return nil
	})
	if err != nil {
		return IssuePage{}, err
	}
	out.ETag, out.NotModified = first.header.Get("ETag"), first.status == http.StatusNotModified
	sort.SliceStable(out.Issues, func(i, j int) bool { return out.Issues[i].UpdatedAt.Before(out.Issues[j].UpdatedAt) })
	return out, nil
}

// CreateIssue opens an issue.
func (c *RESTClient) CreateIssue(ctx context.Context, repo RepoRef, n NewIssue) (Issue, error) {
	if err := validateRepo(repo); err != nil {
		return Issue{}, err
	}
	if strings.TrimSpace(n.Title) == "" {
		return Issue{}, core.NewError(core.CodeInvalidArgument, "an issue needs a title")
	}
	resp, err := c.call(ctx, http.MethodPost, repoPath(repo, "issues"), nil, map[string]any{"title": n.Title, "body": n.Body, "labels": n.Labels}, nil)
	if err != nil {
		return Issue{}, err
	}
	var w wireIssue
	if err := decode(resp, &w); err != nil {
		return Issue{}, err
	}
	return w.issue(repo), nil
}

// SetIssueState opens or closes an issue.
func (c *RESTClient) SetIssueState(ctx context.Context, ref core.IssueRef, state IssueState) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	if state != IssueOpen && state != IssueClosed {
		return core.NewError(core.CodeInvalidArgument, "issue state must be open or closed")
	}
	_, err := c.call(ctx, http.MethodPatch, repoPath(refRepo(ref), "issues", strconv.Itoa(ref.Number)), nil, map[string]string{"state": string(state)}, nil)
	return err
}

// AddLabels adds labels to an issue; labels it already has are unchanged.
func (c *RESTClient) AddLabels(ctx context.Context, ref core.IssueRef, names ...string) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	_, err := c.call(ctx, http.MethodPost, repoPath(refRepo(ref), "issues", strconv.Itoa(ref.Number), "labels"), nil, map[string][]string{"labels": names}, nil)
	return err
}

// RemoveLabel removes one label. A label the issue does not carry is
// reported as NOT_FOUND, like a missing issue.
func (c *RESTClient) RemoveLabel(ctx context.Context, ref core.IssueRef, name string) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	if name == "" {
		return core.NewError(core.CodeInvalidArgument, "a label name is required")
	}
	_, err := c.call(ctx, http.MethodDelete, repoPath(refRepo(ref), "issues", strconv.Itoa(ref.Number), "labels", name), nil, nil, nil)
	return err
}

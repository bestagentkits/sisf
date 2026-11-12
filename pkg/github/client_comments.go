package github

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// ListComments returns the comments of an issue updated at or after since,
// oldest first.
func (c *RESTClient) ListComments(ctx context.Context, ref core.IssueRef, since time.Time) ([]Comment, error) {
	if err := validateRef(ref); err != nil {
		return nil, err
	}
	q := url.Values{"per_page": {"100"}}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339))
	}
	var out []Comment
	_, err := c.getPages(ctx, repoPath(refRepo(ref), "issues", strconv.Itoa(ref.Number), "comments"), q, nil, func(resp apiResponse) error {
		var wire []wireComment
		if err := decode(resp, &wire); err != nil {
			return err
		}
		for _, w := range wire {
			out = append(out, w.comment(ref))
		}
		return nil
	})
	return out, err
}

func validateCommentBody(body string) error {
	if body == "" {
		return core.NewError(core.CodeInvalidArgument, "a comment needs a body")
	}
	if len(body) > maxCommentBytes {
		return core.NewError(core.CodeInvalidArgument, "the comment is longer than GitHub allows")
	}
	return nil
}

// CreateComment adds a comment to an issue or pull request.
func (c *RESTClient) CreateComment(ctx context.Context, ref core.IssueRef, body string) (Comment, error) {
	if err := validateRef(ref); err != nil {
		return Comment{}, err
	}
	if err := validateCommentBody(body); err != nil {
		return Comment{}, err
	}
	resp, err := c.call(ctx, http.MethodPost, repoPath(refRepo(ref), "issues", strconv.Itoa(ref.Number), "comments"), nil, map[string]string{"body": body}, nil)
	if err != nil {
		return Comment{}, err
	}
	var w wireComment
	if err := decode(resp, &w); err != nil {
		return Comment{}, err
	}
	return w.comment(ref), nil
}

// UpdateComment replaces the body of a comment. The issue the comment
// belongs to is not part of GitHub's answer, so the returned comment leaves
// Issue empty.
func (c *RESTClient) UpdateComment(ctx context.Context, repo RepoRef, commentID int64, body string) (Comment, error) {
	if err := validateRepo(repo); err != nil {
		return Comment{}, err
	}
	if commentID < 1 {
		return Comment{}, core.NewError(core.CodeInvalidArgument, "a comment id is required")
	}
	if err := validateCommentBody(body); err != nil {
		return Comment{}, err
	}
	resp, err := c.call(ctx, http.MethodPatch, repoPath(repo, "issues", "comments", strconv.FormatInt(commentID, 10)), nil, map[string]string{"body": body}, nil)
	if err != nil {
		return Comment{}, err
	}
	var w wireComment
	if err := decode(resp, &w); err != nil {
		return Comment{}, err
	}
	return w.comment(core.IssueRef{}), nil
}

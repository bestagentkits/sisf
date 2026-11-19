package github

import (
	"context"
	"net/http"
	"net/url"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Kinds of a tree entry, as GitHub names them.
const (
	TreeBlob   = "blob"
	TreeDir    = "tree"
	TreeCommit = "commit" // a submodule
)

// Modes of a blob entry, as git writes them.
const (
	ModeFile       = "100644"
	ModeExecutable = "100755"
	ModeSymlink    = "120000"
)

// TreeEntry is one entry of a commit's tree: its repository path, kind,
// mode, object name and, for a blob, size in bytes.
type TreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Mode string `json:"mode"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

// ListTree lists every entry of commit's tree, recursively. GitHub cuts a
// listing too large to send whole: that is LIMIT_EXCEEDED, never a part
// taken for the whole, so a caller that must see every file fails closed.
func (c *RESTClient) ListTree(ctx context.Context, repo RepoRef, commit core.CommitSHA) ([]TreeEntry, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if err := commit.Validate(); err != nil {
		return nil, err
	}
	resp, err := c.call(ctx, http.MethodGet, repoPath(repo, "git", "trees", string(commit)), url.Values{"recursive": {"1"}}, nil, nil)
	if err != nil {
		return nil, atStep(err, StepTree)
	}
	var tree struct {
		Tree      []TreeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	if err := decode(resp, &tree); err != nil {
		return nil, atStep(err, StepTree)
	}
	if tree.Truncated {
		return nil, core.NewError(core.CodeLimitExceeded, "GitHub listed only part of the tree of "+string(commit))
	}
	return tree.Tree, nil
}

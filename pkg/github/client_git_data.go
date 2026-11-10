package github

import (
	"context"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// branchNamePattern accepts the branch names the factory itself writes.
var branchNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)

// BranchCommit asks for a commit that replaces Files on top of Base and for
// Branch to point at it. A nil content deletes the path.
type BranchCommit struct {
	Branch  string
	Base    core.CommitSHA
	Message string
	// Author names the identity the commit is made as.
	Author string
	Files  map[string][]byte
}

// validBranch refuses a branch name that could leave refs/heads.
func validBranch(name string) error {
	if !branchNamePattern.MatchString(name) || strings.Contains(name, "..") || strings.HasSuffix(name, "/") ||
		strings.Contains(name, "//") || strings.HasSuffix(name, ".lock") {
		return core.NewError(core.CodeInvalidArgument, "branch "+name+" is not a plain branch name")
	}
	return nil
}

// refPath is the path of a branch ref below the repository.
func refPath(repo RepoRef, kind, branch string) string {
	return repoPath(repo, append([]string{"git", kind, "heads"}, strings.Split(branch, "/")...)...)
}

// BranchHead returns the commit branch points at, or NOT_FOUND.
func (c *RESTClient) BranchHead(ctx context.Context, repo RepoRef, branch string) (core.CommitSHA, error) {
	if err := validateRepo(repo); err != nil {
		return "", err
	}
	if err := validBranch(branch); err != nil {
		return "", err
	}
	resp, err := c.call(ctx, http.MethodGet, refPath(repo, "ref", branch), nil, nil, nil)
	if err != nil {
		return "", err
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := decode(resp, &ref); err != nil {
		return "", err
	}
	sha := core.CommitSHA(ref.Object.SHA)
	return sha, sha.Validate()
}

// CommitToBranch writes the files of bc as one commit on top of its base and
// points its branch at that commit, creating the branch when it does not
// exist. It never touches any other branch. It returns the new commit.
func (c *RESTClient) CommitToBranch(ctx context.Context, repo RepoRef, bc BranchCommit) (core.CommitSHA, error) {
	if err := validateRepo(repo); err != nil {
		return "", err
	}
	if err := validBranch(bc.Branch); err != nil {
		return "", err
	}
	if err := bc.Base.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(bc.Message) == "" || len(bc.Files) == 0 {
		return "", core.NewError(core.CodeInvalidArgument, "a commit needs a message and at least one file")
	}
	var base struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	resp, err := c.call(ctx, http.MethodGet, repoPath(repo, "git", "commits", string(bc.Base)), nil, nil, nil)
	if err == nil {
		err = decode(resp, &base)
	}
	if err != nil {
		return "", err
	}
	entries := make([]map[string]any, 0, len(bc.Files))
	for _, path := range slices.Sorted(maps.Keys(bc.Files)) {
		if !plainPath(path) {
			return "", core.NewError(core.CodeInvalidArgument, "file path "+path+" is not a plain path inside the repository")
		}
		entry := map[string]any{"path": path, "mode": "100644", "type": "blob", "sha": nil}
		if content := bc.Files[path]; content != nil {
			delete(entry, "sha")
			entry["content"] = string(content)
		}
		entries = append(entries, entry)
	}
	var tree, commit struct {
		SHA string `json:"sha"`
	}
	resp, err = c.call(ctx, http.MethodPost, repoPath(repo, "git", "trees"), nil, map[string]any{"base_tree": base.Tree.SHA, "tree": entries}, nil)
	if err == nil {
		err = decode(resp, &tree)
	}
	if err != nil {
		return "", err
	}
	body := map[string]any{"message": bc.Message, "tree": tree.SHA, "parents": []string{string(bc.Base)}}
	if bc.Author != "" {
		body["author"] = map[string]string{"name": bc.Author, "email": "sisf-" + strings.NewReplacer(":", "-", "@", "-").Replace(bc.Author) + "@users.noreply.github.com"}
	}
	resp, err = c.call(ctx, http.MethodPost, repoPath(repo, "git", "commits"), nil, body, nil)
	if err == nil {
		err = decode(resp, &commit)
	}
	if err != nil {
		return "", err
	}
	sha := core.CommitSHA(commit.SHA)
	if err := sha.Validate(); err != nil {
		return "", err
	}
	return sha, c.pointBranch(ctx, repo, bc.Branch, sha)
}

// pointBranch creates branch at sha, or moves it there when it exists.
func (c *RESTClient) pointBranch(ctx context.Context, repo RepoRef, branch string, sha core.CommitSHA) error {
	_, err := c.BranchHead(ctx, repo, branch)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		_, err = c.call(ctx, http.MethodPost, repoPath(repo, "git", "refs"), nil,
			map[string]string{"ref": "refs/heads/" + branch, "sha": string(sha)}, nil)
		return err
	case err != nil:
		return err
	}
	_, err = c.call(ctx, http.MethodPatch, refPath(repo, "refs", branch), nil, map[string]any{"sha": string(sha), "force": true}, nil)
	return err
}

// ChangedPaths lists the paths that differ between base and head, as the
// compare API reports them, sorted.
func (c *RESTClient) ChangedPaths(ctx context.Context, repo RepoRef, base, head core.CommitSHA) ([]string, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if err := base.Validate(); err != nil {
		return nil, err
	}
	if err := head.Validate(); err != nil {
		return nil, err
	}
	resp, err := c.call(ctx, http.MethodGet, repoPath(repo, "compare", string(base)+"..."+string(head)), nil, nil, nil)
	if err != nil {
		return nil, err
	}
	var cmp struct {
		Files []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	}
	if err := decode(resp, &cmp); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(cmp.Files))
	for _, f := range cmp.Files {
		paths = append(paths, f.Filename)
	}
	slices.Sort(paths)
	return paths, nil
}

package github

import (
	"context"
	"fmt"
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

// CommitExists returns nil when repo holds commit, and NOT_FOUND when it
// does not or the repository cannot be seen. Its errors name the step
// StepBaseCommit.
func (c *RESTClient) CommitExists(ctx context.Context, repo RepoRef, commit core.CommitSHA) error {
	if err := validateRepo(repo); err != nil {
		return err
	}
	if err := commit.Validate(); err != nil {
		return err
	}
	_, err := c.call(ctx, http.MethodGet, repoPath(repo, "git", "commits", string(commit)), nil, nil, nil)
	return atStep(err, StepBaseCommit)
}

// FactoryBranchPrefix starts every branch the factory writes: candidate
// branches (sisf/<factory>/<run>-<attempt>), release notes and decision
// proposals. CommitToBranch writes no other branch, so no caller of it can
// move the default branch; only a supervisor merge (Merger) changes that.
const FactoryBranchPrefix = "sisf/"

// CommitToBranch writes the files of bc as one commit on top of its base and
// points its branch at that commit, creating the branch when it does not
// exist. The branch must be one of the factory's own (FactoryBranchPrefix);
// it never touches any other branch. It returns the new commit.
// Every error names the step it came from (ErrorStep), and a path no
// commit can hold (see ValidTreePath) is refused before any write.
func (c *RESTClient) CommitToBranch(ctx context.Context, repo RepoRef, bc BranchCommit) (core.CommitSHA, error) {
	if err := validateRepo(repo); err != nil {
		return "", err
	}
	if err := validBranch(bc.Branch); err != nil {
		return "", err
	}
	if !strings.HasPrefix(bc.Branch, FactoryBranchPrefix) {
		return "", core.NewError(core.CodePermissionDenied, "branch "+bc.Branch+" is not one of the factory's own; only "+FactoryBranchPrefix+"* branches are written")
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
		return "", atStep(err, StepBaseCommit)
	}
	entries := make([]map[string]any, 0, len(bc.Files))
	for _, path := range slices.Sorted(maps.Keys(bc.Files)) {
		if !ValidTreePath(path) {
			return "", contentFault(core.CodeInvalidArgument, StepTree, ContentBadPath, "file path "+path+" is not a path a commit can hold")
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
		return "", atStep(err, StepTree)
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
		return "", atStep(err, StepCommit)
	}
	sha := core.CommitSHA(commit.SHA)
	if err := sha.Validate(); err != nil {
		return "", atStep(err, StepCommit)
	}
	return sha, atStep(c.pointBranch(ctx, repo, bc.Branch, sha), StepBranch)
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
// compare API reports them, sorted. A listing that reaches
// CompareFileLimit may be incomplete: it is returned with LIMIT_EXCEEDED,
// so a caller that must see every path fails closed.
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
	if len(paths) >= CompareFileLimit {
		return paths, core.NewError(core.CodeLimitExceeded, fmt.Sprintf("GitHub's comparison lists at most %d files, so the listing of %s may be incomplete", CompareFileLimit, head))
	}
	return paths, nil
}

// CompareFileLimit is the most files GitHub's comparison lists; a listing
// that long may leave files out.
const CompareFileLimit = 300

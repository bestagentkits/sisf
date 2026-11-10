package wiring

import (
	"context"
	"sync"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// FileReader reads one file of a repository at an exact commit;
// github.RESTClient is one.
type FileReader interface {
	ReadFile(ctx context.Context, repo github.RepoRef, revision core.CommitSHA, path string) ([]byte, error)
}

// maxCachedFiles bounds the files a policy source keeps; the foundation
// files of a few revisions fit many times over.
const maxCachedFiles = 64

// GitHubPolicySource is the production policy.Source: it reads the
// foundation files from the factory's repository on GitHub, at the commit
// the loader names and nowhere else.
//
// What a commit holds never changes, so a file once read is kept: the
// policy is read on every intake step and costs one request per file only
// the first time. A failed read is not kept.
type GitHubPolicySource struct {
	files FileReader
	repo  github.RepoRef

	mu    sync.Mutex
	cache map[cacheKey][]byte
}

type cacheKey struct {
	revision core.CommitSHA
	path     string
}

var _ policy.Source = (*GitHubPolicySource)(nil)

// NewGitHubPolicySource reads the policy of repo through files.
func NewGitHubPolicySource(files FileReader, repo github.RepoRef) (*GitHubPolicySource, error) {
	if files == nil || repo.Owner == "" || repo.Name == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "the policy source needs a GitHub reader and the factory's repository")
	}
	return &GitHubPolicySource{files: files, repo: repo, cache: map[cacheKey][]byte{}}, nil
}

// ReadFile returns path at revision, or NOT_FOUND.
func (s *GitHubPolicySource) ReadFile(ctx context.Context, revision core.CommitSHA, path string) ([]byte, error) {
	key := cacheKey{revision, path}
	s.mu.Lock()
	content, ok := s.cache[key]
	s.mu.Unlock()
	if ok {
		return append([]byte(nil), content...), nil
	}
	content, err := s.files.ReadFile(ctx, s.repo, revision, path)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if len(s.cache) >= maxCachedFiles {
		s.cache = map[cacheKey][]byte{}
	}
	s.cache[key] = append([]byte(nil), content...)
	s.mu.Unlock()
	return content, nil
}

package catalog

import (
	"context"
	"fmt"
	"regexp"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// FileReader reads one file of a repository at an exact commit. The GitHub
// REST client implements it.
type FileReader interface {
	ReadFile(ctx context.Context, repo github.RepoRef, revision core.CommitSHA, path string) ([]byte, error)
}

// Commit asks for a new branch whose tip replaces Files on top of Base.
type Commit struct {
	Branch  string
	Base    core.CommitSHA
	Message string
	// Author is the identity the commit is made as: the Publisher.
	Author string
	Files  map[string][]byte
}

// BranchWriter reads where a branch points and creates a branch with a
// commit on it. It never moves the base branch.
type BranchWriter interface {
	Head(ctx context.Context, repo github.RepoRef, branch string) (core.CommitSHA, error)
	Commit(ctx context.Context, repo github.RepoRef, c Commit) (core.CommitSHA, error)
}

// PullRequestOpener opens a pull request. The GitHub REST client implements it.
type PullRequestOpener interface {
	CreatePullRequest(ctx context.Context, repo github.RepoRef, pr github.NewPullRequest) (github.PullRequest, error)
}

// Config is what a Publisher is built from.
type Config struct {
	Repo       github.RepoRef
	BaseBranch string
	// Actor is the Publisher identity every change is made as.
	Actor    core.Actor
	Files    FileReader
	Branches BranchWriter
	Pulls    PullRequestOpener
}

var branchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)

// Publisher records verified releases through pull requests.
type Publisher struct{ cfg Config }

// New returns a publisher. Only a service actor holding the Publisher role
// may publish: no person's session and no agent writes the catalog.
func New(cfg Config) (*Publisher, error) {
	switch {
	case cfg.Repo.Owner == "" || cfg.Repo.Name == "" || !branchPattern.MatchString(cfg.BaseBranch):
		return nil, core.NewError(core.CodeInvalidArgument, "the publisher needs a repository and a base branch")
	case cfg.Files == nil || cfg.Branches == nil || cfg.Pulls == nil:
		return nil, core.NewError(core.CodeInvalidArgument, "the publisher needs a file reader, a branch writer and a pull request opener")
	case cfg.Actor.Kind != core.ActorService || !cfg.Actor.HasRole(core.RolePublisher) || cfg.Actor.ID == "":
		return nil, core.NewError(core.CodePermissionDenied, "the changelog and the catalog are written only by the Publisher service identity")
	}
	return &Publisher{cfg: cfg}, nil
}

// Publish opens a pull request that adds the release to the changelog and,
// when the entry names a feature, sets that approved feature's status in
// whichever of FEATURES.md and features.yaml the repository has. It returns
// the pull request reference, or "" when the base branch already records
// the release.
func (p *Publisher) Publish(ctx context.Context, e Entry) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	repo := p.cfg.Repo
	base, err := p.cfg.Branches.Head(ctx, repo, p.cfg.BaseBranch)
	if err != nil {
		return "", err
	}
	files := map[string][]byte{}
	changelog, err := p.read(ctx, base, PathChangelog)
	if err != nil {
		return "", err
	}
	updated, changed := AddChangelogEntry(changelog, e)
	if !changed {
		return "", nil
	}
	files[PathChangelog] = updated
	if e.Feature != "" {
		if err := p.setFeature(ctx, base, e, files); err != nil {
			return "", err
		}
	}
	branch := "sisf/release/" + string(e.RunID)
	message := fmt.Sprintf("feat(release): record %s for %s", e.Unit, e.IssueID)
	if _, err := p.cfg.Branches.Commit(ctx, repo, Commit{Branch: branch, Base: base, Message: message, Author: p.cfg.Actor.ID, Files: files}); err != nil {
		return "", err
	}
	pr, err := p.cfg.Pulls.CreatePullRequest(ctx, repo, github.NewPullRequest{
		Title:   fmt.Sprintf("Record the verified release of %s", e.IssueID),
		Body:    fmt.Sprintf("Release %s of %s was observed running and verified.\n\nThis pull request updates the changelog and the feature catalog; the Owner merges it.", e.Release, plain(e.Unit)),
		HeadRef: branch, BaseRef: p.cfg.BaseBranch,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s#pull/%d", repo, pr.Number), nil
}

// setFeature updates the feature in every catalog form the repository has.
// At least one must exist and list the feature.
func (p *Publisher) setFeature(ctx context.Context, base core.CommitSHA, e Entry, files map[string][]byte) error {
	found := false
	for _, form := range []struct {
		path string
		set  func([]byte, string, string) ([]byte, error)
	}{{PathFeatures, SetFeatureStatusMarkdown}, {PathFeaturesYAML, SetFeatureStatusYAML}} {
		content, err := p.read(ctx, base, form.path)
		if err != nil {
			return err
		}
		if len(content) == 0 {
			continue
		}
		updated, err := form.set(content, e.Feature, e.status())
		if err != nil {
			return err
		}
		files[form.path], found = updated, true
	}
	if !found {
		return unapproved(e.Feature)
	}
	return nil
}

// read returns a file at base, or nothing when it does not exist.
func (p *Publisher) read(ctx context.Context, base core.CommitSHA, path string) ([]byte, error) {
	content, err := p.cfg.Files.ReadFile(ctx, p.cfg.Repo, base, path)
	if core.HasCode(err, core.CodeNotFound) {
		return nil, nil
	}
	return content, err
}

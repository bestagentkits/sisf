package stages

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bestagentkits/sisf/pkg/appbundle"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// CandidateTester runs a candidate's test suite isolated from the
// supervisor; SandboxImages is one.
type CandidateTester interface {
	Name() string
	TestCandidate(ctx context.Context, req ImageRequest) (passed bool, evidence string, err error)
}

// CommitFiles reads the files of the factory's repository at a commit: its
// tree, then each blob by the name the tree gives it.
type CommitFiles interface {
	ListTree(ctx context.Context, repo github.RepoRef, commit core.CommitSHA) ([]github.TreeEntry, error)
	ReadBlob(ctx context.Context, repo github.RepoRef, sha string) ([]byte, error)
}

// BundleImages builds a managed application's candidate (ADR 0027): its
// test suite runs in a sandbox, and the supervisor itself assembles the
// content bundle from the candidate's commit as GitHub holds it, so no
// candidate code runs while the bundle is made and nothing the sandbox
// produced goes into it. The release is the pair of the Spec's runtime
// image and that bundle.
type BundleImages struct {
	Tests  CandidateTester
	GitHub CommitFiles
	Repo   github.RepoRef
	// Spec is the Owner-installed Spec: the candidate's own Spec never
	// decides how its bundle is made or which image it runs in.
	Spec  *managedapp.Spec
	Store appbundle.Store
	// Fetcher downloads the archives the lockfile pins into CacheDir.
	Fetcher  appbundle.Fetcher
	CacheDir string
	// WorkDir holds each build's checkout and tar while it is assembled.
	WorkDir string
}

// Name names the builder in build records.
func (b BundleImages) Name() string {
	if b.Tests == nil {
		return "bundle"
	}
	return "bundle:" + b.Tests.Name()
}

// BuildImage tests the candidate, then assembles and stores its bundle.
// An outage of GitHub, the sandbox or an archive's source spends no
// attempt; a bundle the candidate's content makes impossible is refused
// as the candidate's fault.
func (b BundleImages) BuildImage(ctx context.Context, req ImageRequest) (ImageResult, error) {
	if b.Tests == nil {
		return ImageResult{}, Misconfigured(core.NewError(core.CodeInvalidArgument,
			"a managed application's candidate is tested in a sandbox: configure an isolating execution backend"))
	}
	if b.Spec == nil || b.Spec.Bundle.Lock == "" || b.GitHub == nil || b.Fetcher == nil || b.Store.Dir == "" {
		return ImageResult{}, Misconfigured(core.NewError(core.CodeInvalidArgument, "the bundle build needs the installed Spec with a lockfile, GitHub, a fetcher and a bundle store"))
	}
	passed, evidence, err := b.Tests.TestCandidate(ctx, req)
	if err != nil {
		return ImageResult{}, err
	}
	if !passed {
		// A candidate whose tests fail is never released: it is refused
		// before its files are read, and no bundle is made for it.
		return ImageResult{}, refuse(core.CodeGuardFailed, RefusalBuildFailed, "", "the candidate's tests failed: "+evidence)
	}
	work, err := os.MkdirTemp(b.WorkDir, "bundle-*")
	if err != nil {
		return ImageResult{}, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	repoDir := filepath.Join(work, "repo")
	if err := b.checkout(ctx, req.Commit, repoDir); err != nil {
		return ImageResult{}, err
	}
	out := filepath.Join(work, "bundle.tar")
	res, err := appbundle.Assemble(ctx, appbundle.SpecInput(b.Spec, repoDir, b.Fetcher, b.CacheDir, out))
	switch {
	case err != nil && (ctx.Err() != nil || errors.Is(err, appbundle.ErrUnavailable)):
		return ImageResult{}, err
	case err != nil:
		return ImageResult{}, bundleRejected("", "the content bundle cannot be assembled: "+err.Error())
	}
	digest, err := b.store(out)
	if err != nil {
		return ImageResult{}, err
	}
	if digest != res.Digest {
		return ImageResult{}, fmt.Errorf("the stored bundle %s is not the assembled %s", digest, res.Digest)
	}
	image := b.Spec.Runtime.Image
	return ImageResult{
		Digest: core.Digest(deploy.PairDigest(image, digest)), Image: image, Bundle: digest,
		TestsPassed: passed, TestEvidence: evidence,
		ProgramUnread: "a managed application is released as a runtime image and a content bundle; no program is exported",
	}, nil
}

func (b BundleImages) store(tarPath string) (string, error) {
	f, err := os.Open(tarPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	return b.Store.Put(f, appbundle.DefaultLimits)
}

// checkout writes into dir the files of commit the bundle is made from:
// the lockfile and the Spec's sources. A link or a submodule among them is
// refused, as is a tree past the bundle's limits.
func (b BundleImages) checkout(ctx context.Context, commit core.CommitSHA, dir string) error {
	entries, err := b.GitHub.ListTree(ctx, b.Repo, commit)
	switch {
	case core.HasCode(err, core.CodeLimitExceeded):
		return bundleRejected("", "the candidate's tree is too large to list whole")
	case err != nil:
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	limits := appbundle.DefaultLimits
	var files int
	var total int64
	for _, e := range entries {
		if !b.wanted(e.Path) {
			continue
		}
		switch {
		case e.Type == github.TreeDir:
			if err := root.MkdirAll(e.Path, 0o755); err != nil {
				return bundleRejected(e.Path, "the path cannot be made: "+err.Error())
			}
			continue
		case e.Type != github.TreeBlob:
			return bundleRejected(e.Path, "the bundle's sources hold a submodule")
		case e.Mode == github.ModeSymlink:
			return bundleRejected(e.Path, "the bundle's sources hold a symbolic link")
		}
		files++
		total += e.Size
		if files > limits.MaxFiles || e.Size > limits.MaxFileBytes || e.Size > github.MaxBlobBytes || total > limits.MaxTotalBytes {
			return bundleRejected(e.Path, "the bundle's sources are past the bundle's limits")
		}
		data, err := b.GitHub.ReadBlob(ctx, b.Repo, e.SHA)
		switch {
		case core.HasCode(err, core.CodeInvalidArgument) || core.HasCode(err, core.CodeNotFound) || core.HasCode(err, core.CodeLimitExceeded):
			return bundleRejected(e.Path, "the file cannot be read from the candidate: "+err.Error())
		case err != nil:
			return err
		}
		if err := root.MkdirAll(path.Dir(e.Path), 0o755); err != nil {
			return bundleRejected(e.Path, "the path cannot be made: "+err.Error())
		}
		mode := os.FileMode(0o644)
		if e.Mode == github.ModeExecutable {
			mode = 0o755
		}
		if err := root.WriteFile(e.Path, data, mode); err != nil {
			return bundleRejected(e.Path, "the file cannot be written: "+err.Error())
		}
	}
	return nil
}

// wanted reports whether the bundle is made from p: the lockfile, or a
// source or a path under one.
func (b BundleImages) wanted(p string) bool {
	if p == b.Spec.Bundle.Lock {
		return true
	}
	for _, s := range b.Spec.Bundle.Sources {
		from := strings.TrimSuffix(path.Clean(s.From), "/")
		if p == from || strings.HasPrefix(p, from+"/") {
			return true
		}
	}
	return false
}

func bundleRejected(p, message string) *PatchRefusal {
	return refuse(core.CodeGuardFailed, RefusalBundleRejected, p, message)
}

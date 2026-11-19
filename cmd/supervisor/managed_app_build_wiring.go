package main

import (
	"strings"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/intake/prgate"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/appbundle"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Directories of the trust volume the bundle build keeps: the archives the
// lockfile pins, by sha256, and each build's checkout while it runs.
const (
	bundleArchivesRecord = "bundle-archives"
	bundleWorkRecord     = "bundle-work"
)

// archiveTimeout bounds one download of an archive the lockfile pins.
const archiveTimeout = 5 * time.Minute

// managedImages is the build of a managed application's candidates: the
// candidate's tests in the Docker sandbox, then the bundle assembled by
// the supervisor from the commit on GitHub (stages.BundleImages). Nil
// without a managed application. The candidate's code runs only in an
// isolating sandbox, so the bundle build is refused without one.
func (l *lifecycleFlags) managedImages(m *manifest.Manifest, gate *prgate.Gate, docker execution.Backend, gh stages.CommitFiles, trustDir string) (stages.ImageBuilder, error) {
	app, err := l.app.load(l.deploy)
	if err != nil || app == nil {
		return nil, err
	}
	if docker == nil {
		return nil, stages.Misconfigured(core.NewError(core.CodeInvalidArgument, "a managed application's candidates are tested in the Docker sandbox: set --exec docker"))
	}
	if len(strings.Fields(l.buildCommand)) == 0 || l.runnerImage == "" {
		return nil, stages.Misconfigured(core.NewError(core.CodeInvalidArgument, "a managed application's tests need --build-command and --runner-image"))
	}
	if app.Spec.Bundle.Lock == "" {
		return nil, stages.Misconfigured(core.NewError(core.CodeNotSupported, "the Spec's bundle is a program; the factory builds only a bundle pinned by a lockfile"))
	}
	store, err := bundleStore(trustDir)
	if err != nil {
		return nil, err
	}
	cache, err := supervisor.TrustDirectory(trustDir, bundleArchivesRecord)
	if err != nil {
		return nil, err
	}
	work, err := supervisor.TrustDirectory(trustDir, bundleWorkRecord)
	if err != nil {
		return nil, err
	}
	tests := stages.SandboxImages{
		Exec: docker, Argv: strings.Fields(l.buildCommand), Timeout: l.buildTimeout,
		Specs: stages.SandboxSpecs{Image: l.runnerImage, Egress: m.Egress, Limits: execution.Limits{Timeout: l.buildTimeout}, Gate: gate},
	}
	return stages.BundleImages{
		Tests: tests, GitHub: gh, Repo: github.RepoRef{Owner: m.Repository.Owner, Name: m.Repository.Repo}, Spec: app.Spec,
		Store: store, Fetcher: appbundle.NewHTTPFetcher(archiveTimeout, appbundle.DefaultLimits.MaxTotalBytes), CacheDir: cache, WorkDir: work,
	}, nil
}

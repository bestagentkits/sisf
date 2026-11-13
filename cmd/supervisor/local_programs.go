package main

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/supervisor/programstore"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// localPrograms decides what the local deploy backend starts for a
// release. It is the one place a release's image reference becomes a
// program to run:
//
//   - a release whose digest the program store holds starts the store's
//     file, hashed again at this start: the program a candidate build
//     exported and QA judged;
//   - the release the root-signed release manifest pins starts the
//     installed program;
//   - anything else starts nothing. The installed program is never started
//     under a digest that is not its own.
//
// In the simulation, whose scripted builds make no program, there is no
// store and every release starts the installed program, as before.
type localPrograms struct {
	// launcher is --deploy-local-program: started with the image reference
	// and, for a program of the store, that program's file.
	launcher string
	// store holds the programs builds exported; nil where none are kept.
	store *programstore.Store
	// installed is the digest of the installed program, from the release
	// manifest the Owner's root key signed.
	installed core.Digest
}

// openLocalPrograms sets what the local deploy backend runs. A factory
// keeps programs when it deploys locally and builds candidates in a
// sandbox: every one but the simulation with its scripted agent, whose
// image builder runs nothing.
func (l *lifecycleFlags) openLocalPrograms(ctx context.Context, trustDir string, sim bool, book *release.LKGBook) error {
	l.programs = &localPrograms{launcher: l.localProgram}
	if l.deploy != "local" || (sim && l.coderAgent == "fake") {
		return nil
	}
	store, err := programstore.Open(trustDir)
	if err != nil {
		return err
	}
	installed, found, err := book.Installed(ctx, adoptedUnit)
	if err != nil {
		return err
	}
	l.programs.store = store
	if found {
		l.programs.installed = core.Digest(installed)
	}
	return nil
}

// buildStore is where candidate builds export their programs to; nil
// where none are kept.
func (p *localPrograms) buildStore() *programstore.Store {
	if p == nil {
		return nil
	}
	return p.store
}

// command is the local starter's resolver: the program and arguments a
// release's image reference is started with.
func (p *localPrograms) command(image string) (string, []string, error) {
	if p.launcher == "" || !filepath.IsAbs(p.launcher) {
		return "", nil, core.NewError(core.CodeInvalidArgument, "deploy local needs an absolute --deploy-local-program")
	}
	if !imageRefPattern.MatchString(image) {
		return "", nil, core.NewError(core.CodeInvalidArgument, "the release's image reference is not one a local program accepts")
	}
	if p.store == nil {
		return p.launcher, []string{image}, nil
	}
	at := strings.LastIndex(image, "@")
	digest := core.Digest(image[at+1:])
	if at < 0 || digest.Validate() != nil {
		return "", nil, core.NewError(core.CodeInvalidArgument, "the release's image reference is not pinned to a digest, so no program can be told to be it")
	}
	path, err := p.store.Path(digest)
	switch {
	case err == nil:
		return p.launcher, []string{image, path}, nil
	case !core.HasCode(err, core.CodeNotFound):
		return "", nil, err
	case digest == p.installed:
		return p.launcher, []string{image}, nil
	}
	return "", nil, core.NewError(core.CodeNotFound, "the release "+string(digest)+
		" is neither a program a candidate build exported to the supervisor nor the installed release; nothing is started for it")
}

// ObserveArtifact measures what the local deploy backend would start for a
// release, so the release pipeline verifies the bytes and not the digest
// the artifact carries: for a program of the store, the sha256 of the file
// it holds now; for the release the root-signed release manifest pins, the
// installed program, whose digest that manifest gives. Anything else is
// NOT_FOUND, as command starts nothing for it. A factory that keeps no
// programs observes nothing and says so.
func (p *localPrograms) ObserveArtifact(_ context.Context, a deploy.Artifact) (core.ReleaseDigest, error) {
	if p == nil || p.store == nil {
		return "", core.NewError(core.CodeNotSupported, "this factory keeps no programs, so the bytes a release would run cannot be observed")
	}
	digest := core.Digest(a.Digest)
	measured, err := p.store.Measure(digest)
	switch {
	case err == nil:
		return core.ReleaseDigest(measured), nil
	case !core.HasCode(err, core.CodeNotFound):
		return "", err
	case digest == p.installed:
		return core.ReleaseDigest(p.installed), nil
	}
	return "", err
}

// artifactObserver is what the release pipeline measures releases with:
// the program store where programs are kept, and in the simulation with
// its scripted agent, which builds no program, the simulation's own
// observer. Any other factory builds nothing it could observe, and is
// refused.
func (l *lifecycleFlags) artifactObserver(sim bool) (release.ArtifactObserver, error) {
	if l.programs.buildStore() != nil {
		return l.programs, nil
	}
	if sim && l.coderAgent == "fake" {
		return stages.SimulatedArtifacts{}, nil
	}
	return nil, core.NewError(core.CodeNotSupported, "this factory keeps no programs, so the supervisor cannot observe what a release would run")
}

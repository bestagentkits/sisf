package stages

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// Repository is the factory's repository as the builder uses it.
type Repository interface {
	// ReadFile returns a file at a commit, NOT_FOUND when it has none.
	ReadFile(ctx context.Context, repo github.RepoRef, revision core.CommitSHA, path string) ([]byte, error)
	// CommitExists returns nil when the repository holds the commit.
	CommitExists(ctx context.Context, repo github.RepoRef, commit core.CommitSHA) error
	CommitToBranch(ctx context.Context, repo github.RepoRef, bc github.BranchCommit) (core.CommitSHA, error)
}

// ImageRequest asks for the image of one candidate commit.
type ImageRequest struct {
	FactoryID core.FactoryID
	RunID     core.RunID
	Unit      string
	Commit    core.CommitSHA
	Branch    string
	// Untrusted is set for code an agent or an outside contributor wrote.
	Untrusted bool
}

// ImageResult is a finished image build and the suite run with it.
type ImageResult struct {
	Digest        core.Digest
	Image         string
	TestsPassed   bool
	TestEvidence  string
	SBOMRef       string
	ProvenanceRef string
}

// ImageBuilder turns a candidate commit into an immutable image.
type ImageBuilder interface {
	Name() string
	BuildImage(ctx context.Context, req ImageRequest) (ImageResult, error)
}

// BuildRequest is the patch of one run to build.
type BuildRequest struct {
	Issue   core.IssueRef
	RunID   core.RunID
	Attempt int
	Base    core.CommitSHA
	Diff    string
}

// Built is a candidate commit on the factory's branch and its image.
type Built struct {
	Record       handoff.BuildRecord
	Branch       string
	ChangedPaths []string
}

// CandidateBuilder applies a run's patch on top of the base commit, commits
// the result to a branch of this factory only, and builds its image. It
// runs in the supervisor, which holds the GitHub credentials and drives the
// sandboxes; the agent zone has neither.
type CandidateBuilder struct {
	FactoryID core.FactoryID
	Repo      github.RepoRef
	Git       Repository
	Images    ImageBuilder
	Units     UnitRouter
	Clock     core.Clock
}

// Branch is the branch a candidate of run's attempt is committed to: it
// names the factory, so two factories on one upstream never share one.
func Branch(factory core.FactoryID, run core.RunID, attempt int) string {
	return fmt.Sprintf("sisf/%s/%s-%d", factory, run, attempt)
}

// Build builds the patch of req. A patch that cannot be read, does not
// apply, spans units, touches a protected path or names a path no commit
// can hold is refused with a PatchRefusal before anything is committed or
// pushed, as is one whose content GitHub refuses (see statusOutcomes). A
// patch whose build command ran in the sandbox and failed, timed out or
// reported no image is refused after its commit, and Built then names that
// commit. Any other error is an outage of GitHub, the image builder or the
// configuration.
func (b *CandidateBuilder) Build(ctx context.Context, req BuildRequest) (Built, error) {
	patches, err := parseUnifiedDiff(req.Diff)
	if err != nil {
		return Built{}, refuse(core.CodeInvalidArgument, RefusalUnreadable, "", "the run's patch cannot be read: "+err.Error())
	}
	for _, fp := range patches {
		for _, p := range []string{fp.oldPath, fp.newPath} {
			if kind, protected := ProtectedPath(p); protected {
				return Built{}, refuse(core.CodePermissionDenied, RefusalProtectedPath, p,
					"the run's patch changes "+p+" ("+string(kind)+"), which an agent's patch may not touch")
			}
			if p != "" && !github.ValidTreePath(p) {
				return Built{}, refuse(core.CodeInvalidArgument, RefusalPathRejected, p,
					"the run's patch names "+p+", which no commit can hold")
			}
		}
	}
	files := map[string][]byte{}
	for _, fp := range patches {
		old := ""
		if fp.oldPath != "" {
			content, err := b.Git.ReadFile(ctx, b.Repo, req.Base, fp.oldPath)
			if err != nil {
				return Built{}, b.readFault(ctx, req.Base, fp.oldPath, err)
			}
			old = string(content)
		}
		updated, err := fp.apply(old)
		if err != nil {
			return Built{}, refuse(core.CodeConflict, RefusalDoesNotApply, fp.newPath, "the run's patch does not apply to the base: "+err.Error())
		}
		if fp.newPath == "" {
			files[fp.oldPath] = nil
			continue
		}
		if fp.oldPath != "" && fp.oldPath != fp.newPath {
			files[fp.oldPath] = nil
		}
		files[fp.newPath] = []byte(updated)
	}
	changed := slices.Sorted(maps.Keys(files))
	unit, err := b.Units.UnitOf(changed)
	if err != nil {
		return Built{}, refuse(core.CodeInvalidArgument, RefusalUnitSpan, "", err.Error())
	}
	branch := Branch(b.FactoryID, req.RunID, req.Attempt)
	commit, err := b.Git.CommitToBranch(ctx, b.Repo, github.BranchCommit{
		Branch: branch, Base: req.Base, Author: "service:builder",
		Message: fmt.Sprintf("Candidate of %s for %s, attempt %d", req.RunID, req.Issue, req.Attempt), Files: files,
	})
	if err != nil {
		return Built{}, b.commitFault(ctx, req.Base, err)
	}
	image, err := b.Images.BuildImage(ctx, ImageRequest{
		FactoryID: b.FactoryID, RunID: req.RunID, Unit: unit, Commit: commit, Branch: branch, Untrusted: true,
	})
	if err == nil && (image.Digest.Validate() != nil || image.Image == "") {
		err = refuse(core.CodeGuardFailed, RefusalNoImageReported, "", "the build of the candidate reported no image and digest")
	}
	if _, refused := RefusalOf(err); refused {
		// The commit exists and the build failed on it: the refusal names
		// the commit and branch it was run on.
		return Built{Branch: branch, ChangedPaths: changed, Record: handoff.BuildRecord{
			Commit: commit, Unit: unit, Builder: b.Images.Name(), BuiltAt: b.Clock.Now().UTC(),
		}}, err
	}
	if err != nil {
		return Built{}, err
	}
	return Built{Branch: branch, ChangedPaths: changed, Record: handoff.BuildRecord{
		Commit: commit, Digest: image.Digest, Unit: unit, Image: image.Image, Builder: b.Images.Name(),
		TestsPassed: image.TestsPassed, TestEvidence: image.TestEvidence,
		SBOMRef: image.SBOMRef, ProvenanceRef: image.ProvenanceRef, BuiltAt: b.Clock.Now().UTC(),
	}}, nil
}

// readFault decides an error reading a base file the patch edits. A file
// the base lacks is the patch's fault only once the base commit itself has
// been read, as GitHub answers a repository the token cannot see alike.
func (b *CandidateBuilder) readFault(ctx context.Context, base core.CommitSHA, path string, err error) error {
	reason, classified := classifyRepositoryError(err, path)
	if github.ErrorStatus(err) == 0 && core.HasCode(err, core.CodeNotFound) {
		reason = checkBase
	}
	if reason != checkBase {
		return classified
	}
	if baseErr := b.Git.CommitExists(ctx, b.Repo, base); baseErr != nil {
		_, classified = classifyRepositoryError(baseErr, "")
		return classified
	}
	return refuse(core.CodeConflict, RefusalDoesNotApply, path, "the run's patch changes "+path+", which the base does not have")
}

// commitFault decides an error committing the patch's files. GitHub
// answers 422 on the tree both for paths it will not hold and for a base
// tree that is no longer there, so, as for a base file, the refusal is
// charged to the patch only once the base commit itself has been read.
func (b *CandidateBuilder) commitFault(ctx context.Context, base core.CommitSHA, err error) error {
	reason, classified := classifyRepositoryError(err, "")
	if reason != RefusalPathRejected || github.ErrorStep(err) != github.StepTree || github.ErrorStatus(err) == 0 {
		return classified
	}
	if baseErr := b.Git.CommitExists(ctx, b.Repo, base); baseErr != nil {
		_, classified = classifyRepositoryError(baseErr, "")
	}
	return classified
}

// UnitRouter decides which runtime unit a change is a build of, from the
// inputs of each unit (release.UnitOfPath): a path is the input of at most
// one unit, and a path that is the input of none decides nothing.
type UnitRouter struct{}

// DefaultUnitRouter returns the router of the units' inputs.
func DefaultUnitRouter() UnitRouter { return UnitRouter{} }

// judgedOnlyUnit is the unit a change that is the input of no unit is built
// as. The build exists only so that QA judges the candidate and its pull
// request is opened for the Owner: the release refuses such a candidate for
// the Owner's approval, since none of its paths is an input of the unit it
// would release.
const judgedOnlyUnit = "app"

// UnitOf returns the one unit a change set is a build of. A change set
// that changes the inputs of two units is refused: one release replaces one
// unit, so the rest of the change would never be deployed or observed. A
// change set that changes the input of no unit is built as judgedOnlyUnit,
// for the Owner to merge by hand once QA judged it.
func (UnitRouter) UnitOf(paths []string) (string, error) {
	if len(paths) == 0 {
		return "", core.NewError(core.CodeInvalidArgument, "the change touches no path")
	}
	unit, first := "", ""
	for _, p := range paths {
		of, ok := release.UnitOfPath(p)
		if !ok {
			continue
		}
		if unit != "" && of != unit {
			return "", core.NewError(core.CodeInvalidArgument, "the change spans the units "+unit+" ("+first+") and "+of+" ("+p+
				"); one candidate changes the inputs of one unit")
		}
		if unit == "" {
			unit, first = of, p
		}
	}
	if unit == "" {
		return judgedOnlyUnit, nil
	}
	return unit, nil
}

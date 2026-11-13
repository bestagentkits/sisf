package stages

import (
	"context"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// BaseKeeper accepts the commit a run is built, judged and released on,
// checks main before the supervisor merges into it, and moves the commit
// when the supervisor merged a verified release: policy.BuildBases.
type BaseKeeper interface {
	Accept(ctx context.Context, base core.CommitSHA) (core.CommitSHA, error)
	CheckMain(ctx context.Context, actor core.Actor, check policy.MainCheck) error
	Follow(ctx context.Context, actor core.Actor, proof policy.FollowProof) (policy.BuildBase, error)
}

// acceptBase checks that base is a commit a run of the policy refs name may
// be built, judged and released on, and returns the commit accepted. With a
// keeper every base is asked, the policy revision included, so a factory
// that stopped building on main accepts none; without one only the policy
// revision is accepted. The commit returned is the only value a proof may
// name as the base: a caller that skips this check has none to give.
func acceptBase(ctx context.Context, keeper BaseKeeper, refs core.PolicyRefs, base core.CommitSHA) (core.CommitSHA, error) {
	if keeper != nil {
		return keeper.Accept(ctx, base)
	}
	if base != refs.PolicySHA || base.Validate() != nil {
		return "", core.NewError(core.CodePolicyHold, "the run names another commit to build on than the policy revision "+string(refs.PolicySHA))
	}
	return base, nil
}

// releaseBases is the release pipeline's view of the keeper: it accepts
// the base a verified candidate is released on.
type releaseBases struct{ keeper BaseKeeper }

// Accept accepts base again, as the build and QA stages did.
func (r releaseBases) Accept(ctx context.Context, base core.CommitSHA) error {
	_, err := r.keeper.Accept(ctx, base)
	return err
}

// CommitGraph names the parents of the factory's commits on GitHub.
type CommitGraph struct {
	GitHub interface {
		CommitParents(ctx context.Context, repo github.RepoRef, commit core.CommitSHA) ([]core.CommitSHA, error)
	}
	Repo github.RepoRef
}

// CommitParents returns commit's parents, its first parent first.
func (g CommitGraph) CommitParents(ctx context.Context, commit core.CommitSHA) ([]core.CommitSHA, error) {
	return g.GitHub.CommitParents(ctx, g.Repo, commit)
}

// mainFollower is the release pipeline's Follower: the supervisor moves the
// commit to build on as itself.
type mainFollower struct{ keeper BaseKeeper }

// Follow follows the merge of a verified release.
func (f mainFollower) Follow(ctx context.Context, req release.FollowRequest) error {
	_, err := f.keeper.Follow(ctx, policy.SupervisorActor(),
		policy.FollowProof{Merge: req.Merge, Candidate: req.Candidate, Run: req.RunID, Unit: req.Unit})
	return err
}

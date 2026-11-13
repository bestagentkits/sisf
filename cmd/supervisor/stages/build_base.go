package stages

import (
	"context"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
)

// BaseKeeper accepts the commit a run is built and judged on, and moves
// it when the supervisor merged a verified release: policy.BuildBases.
type BaseKeeper interface {
	Accept(ctx context.Context, base core.CommitSHA) error
	Follow(ctx context.Context, actor core.Actor, proof policy.FollowProof) (policy.BuildBase, error)
}

// acceptBase checks that base is a commit a run of the policy refs name may
// be built and judged on. Without a keeper only the policy revision is.
func acceptBase(ctx context.Context, keeper BaseKeeper, refs core.PolicyRefs, base core.CommitSHA) error {
	if base == refs.PolicySHA {
		return nil
	}
	if keeper == nil {
		return core.NewError(core.CodePolicyHold, "the run names another commit to build on than the policy revision "+string(refs.PolicySHA))
	}
	return keeper.Accept(ctx, base)
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

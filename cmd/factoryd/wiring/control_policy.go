package wiring

import (
	"context"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// publishPolicy publishes the references of the approved revision once,
// and forgets the commit to build on of the revision it replaced.
func (c *Control) publishPolicy(ctx context.Context) error {
	baseline, err := c.Baselines.Current(ctx)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return nil // nothing approved yet: intake holds, and so does the agent zone
	case err != nil:
		return err
	}
	c.mu.Lock()
	done := c.published == baseline.Revision
	c.mu.Unlock()
	if done {
		return nil
	}
	snapshot, err := c.Policy.Current(ctx)
	if err != nil {
		return err
	}
	if err := PublishPolicyRefs(ctx, c.cfg.Store, snapshot); err != nil {
		return err
	}
	// A stop of the supervisor's commit to build on that a revision the
	// Owner since approved answered holds nothing any more.
	if err := policy.ClearStaleBuildBase(ctx, c.cfg.Store, snapshot.Revision); err != nil {
		return err
	}
	c.mu.Lock()
	c.published = snapshot.Revision
	c.mu.Unlock()
	return nil
}

// LocalOwner is the Owner acting from the factory's own host, through the
// factoryd command line. Running it needs the control zone's secrets, which
// only the Owner of the install can read; the identity was just proven by
// that access, so it counts as a fresh step-up.
func LocalOwner(clock core.Clock) core.Actor {
	return core.Actor{ID: "owner:local", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner},
		Surface: core.SurfaceCLI, StepUpAt: clock.Now()}
}

// ApproveBaseline makes revision the policy in force, as the Owner. The
// first revision needs nothing more; replacing one needs approvalID, an
// approval of exactly the new content.
func (c *Control) ApproveBaseline(ctx context.Context, revision core.CommitSHA, approvalID string) (policy.Baseline, error) {
	return c.Baselines.Approve(ctx, LocalOwner(c.cfg.Clock), revision, approvalID)
}

// RequestPolicyChange files, as the Owner, the approval request that making
// revision the policy in force needs, and returns it. Approving it and then
// ApproveBaseline with its ID puts the revision in force.
func (c *Control) RequestPolicyChange(ctx context.Context, revision core.CommitSHA) (approvals.Request, error) {
	return c.Baselines.RequestChange(ctx, LocalOwner(c.cfg.Clock), revision)
}

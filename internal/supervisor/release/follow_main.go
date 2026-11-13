package release

import (
	"context"
	"errors"

	"github.com/bestagentkits/sisf/pkg/core"
)

// FollowRequest names the merge of a verified release the commit to build
// on may follow.
type FollowRequest struct {
	RunID core.RunID
	Unit  string
	// Merge is the merge commit the Lander made, empty when it is not
	// known; Candidate the commit QA judged.
	Merge     core.CommitSHA
	Candidate core.CommitSHA
}

// Follower moves the commit to build on to the merge of a verified release
// when the content the Owner approved is unchanged there. It returns
// APPROVAL_REQUIRED when it may not, which stops the factory until the
// Owner approves a revision, and any other error when nothing could be
// decided now.
type Follower interface {
	Follow(ctx context.Context, req FollowRequest) error
}

// waitsFollow words a follow that could not be decided now.
const waitsFollow = "following the merge on main could not be decided (%s); this is tried again on every pass"

// follow has the commit to build on follow a verified release's merge, so
// the next release of its unit is built on a commit that contains this
// one. It returns a note for the issue when the factory stopped instead.
// A superseded release's change does not serve and is not followed.
func (p *Pipeline) follow(ctx context.Context, f *finishing, landing Landing) (string, error) {
	if p.cfg.Follower == nil || !landing.Merged || f.superseded != "" {
		return "", nil
	}
	err := p.cfg.Follower.Follow(ctx, FollowRequest{RunID: f.due.RunID, Unit: f.due.Unit, Merge: landing.Commit, Candidate: f.receipt.CandidateSHA})
	switch {
	case err == nil:
		return "", nil
	case core.HasCode(err, core.CodeApprovalRequired):
		return "the factory stopped building on main: " + messageOf(err), nil
	}
	return "", err
}

// messageOf returns the caller-safe message of a typed error.
func messageOf(err error) string {
	var e *core.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return string(core.CodeOf(err))
}

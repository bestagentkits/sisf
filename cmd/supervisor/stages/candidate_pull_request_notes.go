package stages

import (
	"context"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// leaveRolledBack notes on the pull request of a run whose release was
// rolled back that the factory will not merge it, then records that.
func (c *CandidatePRs) leaveRolledBack(ctx context.Context, run core.RunID) error {
	var pr handoff.CandidatePR
	if err := c.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		pr, err = handoff.ReadCandidatePR(ctx, tx, run)
		return err
	}); err != nil {
		return err
	}
	if err := c.note(ctx, pr, "The release built from this pull request was rolled back. The factory will not merge it; it stays open for the Owner."); err != nil {
		return err
	}
	pr.State, pr.Reason, pr.DecidedAt = handoff.LandingRolledBack, "the release was rolled back", c.Clock.Now().UTC()
	return c.State.Update(ctx, func(ctx context.Context, tx state.Tx) error { return handoff.PutCandidatePR(ctx, tx, pr) })
}

// note comments on the pull request once per run. Only the factory's own
// comment counts as the note already made: anyone can copy its marker.
func (c *CandidatePRs) note(ctx context.Context, pr handoff.CandidatePR, text string) error {
	ref := core.IssueRef{Owner: c.Repo.Owner, Repo: c.Repo.Name, Number: pr.Number}
	marker := "<!-- sisf:landing " + string(pr.RunID) + " -->"
	self, err := c.factoryAccount(ctx)
	if err != nil {
		return err
	}
	comments, err := c.GitHub.ListComments(ctx, ref, time.Time{})
	if err != nil {
		return err
	}
	for _, cm := range comments {
		if sameAccount(self, cm.Author) && strings.Contains(cm.Body, marker) {
			return nil
		}
	}
	_, err = c.GitHub.CreateComment(ctx, ref, text+"\n\n"+marker)
	return err
}

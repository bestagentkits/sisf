package stages

import (
	"context"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// leaveRolledBack notes on the pull request of a run whose release was
// rolled back, refused before anything was deployed, or verified and then
// no longer judged to serve, that the factory will not merge it, then
// records that. The run's stop record says which: a refused release was
// never deployed and a verified one was not undone for failing its window,
// so neither is called rolled back. What the landing waited on is cleared.
func (c *CandidatePRs) leaveRolledBack(ctx context.Context, run core.RunID) error {
	var pr handoff.CandidatePR
	var stop handoff.RunStop
	if err := c.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		if pr, err = handoff.ReadCandidatePR(ctx, tx, run); err != nil {
			return err
		}
		read, err := handoff.ReadRunStop(ctx, tx, run)
		switch {
		case core.HasCode(err, core.CodeNotFound):
			return nil
		case err != nil:
			return err
		}
		stop = read
		return nil
	}); err != nil {
		return err
	}
	text, landing, reason := "The release built from this pull request was rolled back. The factory will not merge it; it stays open for the Owner.",
		handoff.LandingRolledBack, "the release was rolled back"
	switch {
	case stop.Cause == statemachine.BlockReleaseRefused:
		text, landing, reason = "The release built from this pull request was refused before anything was deployed. "+
			"The factory will not merge it; it stays open for the Owner.", handoff.LandingRefused, "the release was refused"
	case stop.Cause == statemachine.BlockReleaseUnjudged && stop.Verified:
		text, landing, reason = "The release built from this pull request was verified, but it could then no longer be judged to be what serves, "+
			"so it was not finished. The factory will not merge it; it stays open and unmerged for the Owner.",
			handoff.LandingLeftOpen, "the verified release could no longer be judged to be what serves"
	}
	if err := c.note(ctx, pr, text); err != nil {
		return err
	}
	pr.State, pr.Reason, pr.DecidedAt = landing, reason, c.Clock.Now().UTC()
	pr.Waiting, pr.WaitingSince = "", time.Time{}
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
		if self.wrote(cm.Author) && strings.Contains(cm.Body, marker) {
			return nil
		}
	}
	_, err = c.GitHub.CreateComment(ctx, ref, text+"\n\n"+marker)
	return err
}

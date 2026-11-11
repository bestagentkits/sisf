package wiring

import (
	"context"
	"time"
)

// expireClarifications parks the issues nobody answered in time, once
// every answer given before their deadline was taken in.
func (c *Control) expireClarifications(ctx context.Context) error {
	through, err := c.answersTakenIn(ctx)
	if err != nil || through.IsZero() {
		return err
	}
	parked, err := c.Intake.ExpireClarifications(ctx, through)
	for _, issue := range parked {
		c.log.Info("parked an issue that was not answered in time", "issue", issue.String())
	}
	return err
}

// answersTakenIn returns the time up to which every answer given on GitHub
// is known to intake; zero when no issue waits to be parked.
//
// A webhook may be lost and an install without webhooks has none, so an
// answer is known for sure only once a poll that started after it read the
// listing to its end. When an issue's deadline passed after the start of
// the last poll, as after a pause of the passes or when an answer lands
// within one poll interval of the deadline, GitHub is polled now and what
// it found is handed to intake before anything is parked. A poll that fails
// or leaves a backlog defers the parking: parking late is harmless, parking
// an answered issue is not.
func (c *Control) answersTakenIn(ctx context.Context) (time.Time, error) {
	r := c.reconcile
	if r == nil {
		return c.cfg.Clock.Now(), nil
	}
	due, err := c.Intake.OverdueClarification(ctx)
	if err != nil || due.IsZero() {
		return time.Time{}, err
	}
	if r.complete.Before(due) && r.attempted.Before(due) {
		if err := c.ReconcileGitHub(ctx); err != nil {
			return time.Time{}, err
		}
		if err := c.drainInbox(ctx); err != nil {
			return time.Time{}, err
		}
	}
	return r.complete, nil
}

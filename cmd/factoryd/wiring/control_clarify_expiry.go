package wiring

import (
	"context"
	"errors"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Why the parking of unanswered issues waits, as the factory's status and
// log show it.
const (
	deferredPollFailed   = "GitHub could not be polled for answers"
	deferredPollBacklog  = "the last poll of GitHub did not read its listing to the end"
	deferredDrainFailed  = "the answers polled from GitHub could not be handed to intake"
	deferredInboxWaiting = "deliveries from GitHub wait in the inbox for intake"
)

// expireClarifications parks the issues nobody answered in time, once
// every answer given before their deadline was taken in and handled. While
// that is not known, the parking waits, and the factory's status and log
// say since when and why (see GitHubSync).
func (c *Control) expireClarifications(ctx context.Context) error {
	due, err := c.Intake.OverdueClarification(ctx)
	if err != nil {
		return err
	}
	if due.IsZero() {
		return c.noteParkingDeferred(ctx, time.Time{}, "")
	}
	through, why, err := c.answersTakenIn(ctx, due)
	if err != nil || through.Before(due) {
		return errors.Join(err, c.noteParkingDeferred(ctx, due, why))
	}
	if err := c.noteParkingDeferred(ctx, time.Time{}, ""); err != nil {
		return err
	}
	parked, err := c.Intake.ExpireClarifications(ctx, through)
	for _, issue := range parked {
		c.log.Info("parked an issue that was not answered in time", "issue", issue.String())
	}
	return err
}

// answersTakenIn returns the time up to which every answer given on GitHub
// is known to intake and handled, given the earliest deadline due of an
// issue waiting to be parked, and why it is before due when it is.
//
// A webhook may be lost and an install without webhooks has none, so an
// answer is known for sure only once a poll that started after it read the
// listing to its end. When an issue's deadline passed after the start of
// the last poll, as after a pause of the passes or when an answer lands
// within one poll interval of the deadline, GitHub is polled now and what
// it found is handed to intake before anything is parked. An answer stored
// in the inbox is handled only once intake took it: while any GitHub
// delivery waits there, behind a full batch of the drain or held back after
// a failure, nothing is parked. A poll that fails or leaves a backlog
// defers the parking too: parking late is harmless, parking an answered
// issue is not.
func (c *Control) answersTakenIn(ctx context.Context, due time.Time) (time.Time, string, error) {
	through := c.cfg.Clock.Now()
	if r := c.reconcile; r != nil {
		if r.complete.Before(due) && r.attempted.Before(due) {
			if err := c.ReconcileGitHub(ctx); err != nil {
				return time.Time{}, deferredPollFailed, err
			}
			if err := c.drainInbox(ctx); err != nil {
				return time.Time{}, deferredDrainFailed, err
			}
		}
		through = r.complete
	}
	waiting, err := c.cfg.Store.DeliveriesWaiting(ctx, control.InboxSourceGitHub)
	switch {
	case err != nil:
		return time.Time{}, deferredInboxWaiting, err
	case waiting:
		return time.Time{}, deferredInboxWaiting, nil
	case through.Before(due):
		return through, c.pollDeferral(ctx), nil
	}
	return through, "", nil
}

// pollDeferral says why the last complete poll started before a deadline
// that is due: the last poll failed or stopped at its bound.
func (c *Control) pollDeferral(ctx context.Context) string {
	if c.reconcile == nil {
		return deferredPollBacklog
	}
	st, err := c.reconcile.loadState(ctx)
	if err == nil && st.LastError != "" {
		return deferredPollFailed
	}
	return deferredPollBacklog
}

// noteParkingDeferred records that the parking of issues due since since
// waits, for why; a zero since records that it does not. A change is
// logged once, and kept with the reconciliation state for the factory's
// status. The stored state, not what this process last wrote, decides
// whether it changed, so a deferral left by an earlier process, or one
// whose write failed, is put right by the next pass.
func (c *Control) noteParkingDeferred(ctx context.Context, since time.Time, why string) error {
	c.mu.Lock()
	was, wasWhy := c.parkingDeferred, c.parkingDeferredWhy
	c.parkingDeferred, c.parkingDeferredWhy = since, why
	c.mu.Unlock()
	switch {
	case since.Equal(was) && why == wasWhy:
	case since.IsZero():
		c.log.Info("the parking of unanswered issues goes on", "deferred_since", was)
	default:
		c.log.Warn("the parking of unanswered issues is deferred", "deferred_since", since, "reason", why)
	}
	if r := c.reconcile; r != nil {
		if err := r.recordParkingDeferred(ctx, since, why); err != nil {
			c.log.Warn("the deferral of parking could not be recorded for the status", "code", string(core.CodeOf(err)))
			return err
		}
	}
	return nil
}

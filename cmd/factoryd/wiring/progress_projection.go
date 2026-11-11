package wiring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Viewer names the account a GitHub token acts as; github.RESTClient is
// one. An install on the operator's login learns its own account this way.
type Viewer interface {
	Viewer(ctx context.Context) (github.User, error)
}

// activeStates are the states of a run in flight, whose issue's progress
// comment follows it every pass.
// RolledBack is one of them: a restore always goes on to Blocked with its
// recovery report, which the comment must still show.
var activeStates = []core.State{
	core.StateClaimed, core.StateDeveloping, core.StateQAEvaluation, core.StateReleaseCandidate, core.StateCanary,
	core.StateMonitoring, core.StateVerified, core.StateRolledBack,
}

// endState reports whether s ends a run: its progress is shown once more
// and then left alone.
func endState(s core.State) bool {
	return s == core.StateClosed || s == core.StateBlocked
}

// projectProgress keeps one progress comment on every issue a run claimed,
// edited in place as the run passes its checkpoints. It reads the issues
// of runs in flight, and those that ended since their comment was last
// written (they still have a progress entry); an issue that ended before
// it ever had one, as before this comment existed, is never given one.
// What was last written is remembered per issue, so an issue whose
// progress did not change costs no call to GitHub.
func (c *Control) projectProgress(ctx context.Context) error {
	var records []core.StateRecord
	err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		if records, err = tx.ListRecords(ctx, state.RecordFilter{States: activeStates}); err != nil {
			return err
		}
		entries, err := tx.ListKV(ctx, progressNamespace, "", 0)
		if err != nil {
			return err
		}
		for _, e := range entries {
			entry, ok := decodeProgress(e)
			if !ok {
				continue
			}
			r, err := tx.GetRecord(ctx, entry.Issue)
			if err == nil && endState(r.State) {
				records = append(records, r)
			} else if err != nil && !core.HasCode(err, core.CodeNotFound) {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	var failed []error
	for _, r := range records {
		if err := c.showProgress(ctx, r); err != nil {
			failed = append(failed, fmt.Errorf("show the progress of %s: %w", r.IssueID, err))
		}
	}
	return errors.Join(failed...)
}

// showProgress writes the progress of one issue when it changed. An issue
// whose comment cannot be written is tried again only after a pause that
// doubles each time. An ended issue's last progress is given up only after
// GitHub refused it for good maxProgressFailures times since the run
// ended, a locked issue say, so its close is not held back by it; an
// outage, a rate limit or an error from before the end never gives it up.
func (c *Control) showProgress(ctx context.Context, r core.StateRecord) error {
	if r.RunID == "" {
		return nil
	}
	entry, found, err := c.progressEntry(ctx, r.IssueID)
	if err != nil {
		return err
	}
	if !found && (endState(r.State) || r.State == core.StateRolledBack) {
		// Ended, or rolled back on its way to Blocked, before it had a
		// progress comment; or already shown at its end.
		return nil
	}
	ended := endState(r.State)
	if found && ended && !entry.AtEnd {
		// The end is written at once, with tries of its own.
		entry = progressEntry{Issue: entry.Issue, Digest: entry.Digest, AtEnd: true}
	}
	now := c.cfg.Clock.Now().UTC()
	if found && now.Before(entry.RetryAt) {
		return core.NewError(core.CodeUnavailable, "the progress comment could not be written; it is tried again after "+entry.RetryAt.Format(time.RFC3339))
	}
	facts, err := c.progressFacts(ctx, r)
	if err != nil {
		return err
	}
	body := renderProgress(facts)
	if c.redactor != nil {
		body = c.redactor.Secrets(body)
	}
	c.mu.Lock()
	shown := c.progressShown[r.IssueID] == body
	c.mu.Unlock()
	stored := found && entry.Digest == progressDigest(body)
	if !shown && !stored {
		if err := c.writeProgress(ctx, r.IssueID, body); err != nil {
			entry.Issue, entry.AtEnd, entry.Failures = r.IssueID, ended, entry.Failures+1
			if !ended {
				entry.Refusals = 0
			} else if refusedForGood(err) {
				entry.Refusals++
			}
			entry.RetryAt = now.Add(progressPause(entry.Failures))
			if ended && entry.Refusals >= maxProgressFailures {
				return errors.Join(err, c.dropProgress(ctx, r.IssueID))
			}
			return errors.Join(err, c.storeProgress(ctx, entry))
		}
	}
	c.mu.Lock()
	c.progressShown[r.IssueID] = body
	c.mu.Unlock()
	if ended {
		return c.dropProgress(ctx, r.IssueID)
	}
	return c.storeProgress(ctx, progressEntry{Issue: r.IssueID, Digest: progressDigest(body)})
}

// refusedForGood reports whether GitHub, or the comment writer before it,
// refused a progress write in a way trying again does not change: the
// issue is locked or the token may not comment (403), the issue is gone
// (404, 410), or the comment is refused (422). An outage, a 5xx, a rate
// limit, a refused token or any error of unknown cause is not.
func refusedForGood(err error) bool {
	for _, code := range []core.ErrorCode{core.CodePermissionDenied, core.CodeNotFound, core.CodeInvalidArgument, core.CodeAlreadyExists, core.CodeNotSupported} {
		if core.HasCode(err, code) {
			return true
		}
	}
	return false
}

// progressGivenUp reports whether the issue's progress is no longer
// written: it ended and GitHub refused its comment for good
// maxProgressFailures times, or it never had one.
func (c *Control) progressGivenUp(ctx context.Context, issue core.IssueRef) bool {
	_, found, err := c.progressEntry(ctx, issue)
	return err == nil && !found
}

func (c *Control) writeProgress(ctx context.Context, issue core.IssueRef, body string) error {
	writer, err := c.progressWriter(ctx)
	if err != nil {
		return err
	}
	return c.editIssue(ctx, issue, editIntent{}, func(ctx context.Context) error {
		_, err := writer.Upsert(ctx, issue, body)
		return err
	})
}

// maxProgressFailures is how often GitHub may refuse an ended issue's last
// progress for good before it is given up.
const maxProgressFailures = 3

// progressPause is the pause after the n-th failure in a row to write an
// issue's progress: a minute, doubled each time, at most an hour.
func progressPause(n int) time.Duration {
	pause := time.Minute
	for range min(n-1, 6) {
		pause *= 2
	}
	return min(pause, time.Hour)
}

// progressWriter is the progress comment writer, its account learned from
// GitHub the first time when the install names none.
func (c *Control) progressWriter(ctx context.Context) (*github.ProgressComment, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.progress.Self.IsZero() {
		viewer, ok := c.cfg.GitHub.(Viewer)
		if !ok {
			return nil, core.NewError(core.CodeNotSupported, "the factory's own GitHub account is not known, so its progress comment cannot be told apart")
		}
		u, err := viewer.Viewer(ctx)
		if err != nil {
			return nil, err
		}
		c.progress.Self = github.Identity{ID: u.ID, Login: u.Login}
	}
	return c.progress, nil
}

// progressFacts reads what the progress of r is rendered from.
func (c *Control) progressFacts(ctx context.Context, r core.StateRecord) (progressFacts, error) {
	f := progressFacts{record: r}
	err := c.cfg.Store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		if entry, err := tx.GetKV(ctx, handoff.Namespace, "run/"+string(r.RunID)); err == nil {
			var run dispatcher.RunRecord
			if json.Unmarshal(entry.Value, &run) == nil && run.RunID == r.RunID {
				f.run = &run
			}
		} else if !core.HasCode(err, core.CodeNotFound) {
			return err
		}
		var err error
		if f.notes, err = handoff.ReadQANotes(ctx, tx, r.RunID); err != nil {
			return err
		}
		if r.State == core.StateQAEvaluation {
			if hold, err := handoff.ReadQAHold(ctx, tx, r.RunID, r.Attempt); err == nil {
				f.hold = &hold
			} else if !core.HasCode(err, core.CodeNotFound) {
				return err
			}
		}
		if r.State == core.StateReleaseCandidate {
			if hold, err := handoff.ReadReleaseHold(ctx, tx, r.RunID, r.Attempt); err == nil {
				f.release = &hold
			} else if !core.HasCode(err, core.CodeNotFound) {
				return err
			}
		}
		if pr, err := handoff.ReadCandidatePR(ctx, tx, r.RunID); err == nil {
			f.pr = &pr
		} else if !core.HasCode(err, core.CodeNotFound) {
			return err
		}
		if entry, err := tx.GetKV(ctx, release.RecoveryNamespace, release.RecoveryKey(r.RunID)); err == nil {
			var report release.RecoveryReport
			if json.Unmarshal(entry.Value, &report) == nil && report.RunID == r.RunID {
				f.restored = report.RestoredTo
			}
		} else if !core.HasCode(err, core.CodeNotFound) {
			return err
		}
		return nil
	})
	return f, err
}

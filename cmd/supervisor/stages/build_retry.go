package stages

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// BuildRetry bounds how a build that failed on an outage is retried.
type BuildRetry struct {
	// First is the wait after the first failure; it doubles with each
	// further failure up to Max.
	First, Max time.Duration
	// HoldAfter is the number of outages in a row after which the build is
	// held: the supervisor stops trying it and tells the Owner, and only
	// the Owner's retry lets it try again.
	HoldAfter int
	// RemindEvery is how long a build stays held before the Owner is
	// reminded on the issue, and again after as long, at most MaxReminders
	// times: a held build does not wait unnoticed behind one comment.
	RemindEvery  time.Duration
	MaxReminders int
}

// DefaultBuildRetry waits 30 seconds after the first outage, at most ten
// minutes between tries, holds the build after five in a row, and reminds
// the Owner of a held build once a day, three times.
var DefaultBuildRetry = BuildRetry{First: 30 * time.Second, Max: 10 * time.Minute, HoldAfter: 5,
	RemindEvery: 24 * time.Hour, MaxReminders: 3}

func (r BuildRetry) settled() BuildRetry {
	d := DefaultBuildRetry
	if r.First > 0 {
		d.First = r.First
	}
	if r.Max > 0 {
		d.Max = r.Max
	}
	if r.HoldAfter > 0 {
		d.HoldAfter = r.HoldAfter
	}
	if r.RemindEvery > 0 {
		d.RemindEvery = r.RemindEvery
	}
	if r.MaxReminders > 0 {
		d.MaxReminders = r.MaxReminders
	}
	d.Max = max(d.Max, d.First)
	return d
}

// backoff is the wait after the n-th failure in a row.
func (r BuildRetry) backoff(n int) time.Duration {
	wait := r.First
	for i := 1; i < n && wait < r.Max; i++ {
		wait *= 2
	}
	return min(wait, r.Max)
}

func (r BuildRetry) held(e retryEntry) bool { return e.Failures >= r.HoldAfter }

// remindDue reports whether a held build is owed its next reminder at now.
func (r BuildRetry) remindDue(e retryEntry, now time.Time) bool {
	return e.Held && !e.HeldAt.IsZero() && e.Reminders < r.MaxReminders &&
		!now.Before(e.HeldAt.Add(time.Duration(e.Reminders+1)*r.RemindEvery))
}

// BuildOutage is the record of the outages one attempt's build met.
type BuildOutage = handoff.BuildOutage

// retryEntry is a stored outage record and its version; zero when none.
type retryEntry struct {
	BuildOutage
	version int64
}

// waiting reports whether the backoff of the last outage has not run out.
func (e retryEntry) waiting(now time.Time) bool { return e.version != 0 && now.Before(e.NextTryAt) }

// ReadBuildOutage reads the outage record of one evaluator attempt,
// NOT_FOUND while its build met none.
func ReadBuildOutage(ctx context.Context, kv state.KV, run core.RunID, attempt int) (BuildOutage, error) {
	o, _, err := handoff.ReadBuildOutage(ctx, kv, run, attempt)
	return o, err
}

func readRetry(ctx context.Context, kv state.KV, run core.RunID, attempt int) (retryEntry, error) {
	o, version, err := handoff.ReadBuildOutage(ctx, kv, run, attempt)
	if core.HasCode(err, core.CodeNotFound) {
		return retryEntry{}, nil
	}
	return retryEntry{BuildOutage: o, version: version}, err
}

func clearRetry(ctx context.Context, tx state.KV, run core.RunID, attempt int) error {
	e, err := readRetry(ctx, tx, run, attempt)
	if err != nil || e.version == 0 {
		return err
	}
	return handoff.DeleteBuildOutage(ctx, tx, run, attempt, e.version)
}

// recordOutage counts one more outage of a patch's build and sets when it
// is tried next. The outage that reaches the limit holds the build and, in
// the same transaction, queues the notice the control zone posts on the
// issue. It returns the record written.
func (s *BuildStage) recordOutage(ctx context.Context, j buildJob, cause error, r BuildRetry) (retryEntry, error) {
	var next retryEntry
	err := s.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		cur, err := readRetry(ctx, tx, j.record.RunID, j.record.Attempt)
		if err != nil {
			return err
		}
		now := s.Clock.Now().UTC()
		next = cur
		next.RunID, next.IssueID, next.Attempt = j.record.RunID, j.record.IssueID, j.record.Attempt
		next.Failures++
		next.LastCode, next.UpdatedAt = core.CodeOf(cause), now
		next.NextTryAt = now.Add(r.backoff(next.Failures))
		next.Reason = ""
		switch {
		case policySuperseded(cause):
			// Trying again changes nothing until the Owner acts.
			next.Reason = handoff.HeldPolicySuperseded
		case baseLacksLKG(cause):
			next.Reason = handoff.HeldBaseLacksRelease
		}
		if (r.held(next) || next.Reason != "") && !cur.Held {
			next.Held, next.HeldAt, next.Reminders = true, now, 0
			next.Holds++
			if handoff.HeldEndsRun(next.Reason) {
				// The run is over: its patch is never built, and the
				// control zone blocks the issue. Its reservation is closed.
				if err := putCloseDue(ctx, tx, j.record.RunID); err != nil {
					return err
				}
			}
			if err := enqueueHeldNotice(ctx, tx, next.BuildOutage); err != nil {
				return err
			}
		}
		next.version, err = handoff.PutBuildOutage(ctx, tx, next.BuildOutage, cur.version)
		return err
	})
	return next, err
}

// remind queues the next reminder of a held build when it is due, with
// the count of reminders in the same write. It reports whether it did.
func (s *BuildStage) remind(ctx context.Context, j buildJob, r BuildRetry, now time.Time) (bool, error) {
	if !r.remindDue(j.retry, now) {
		return false, nil
	}
	err := s.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		cur, err := readRetry(ctx, tx, j.record.RunID, j.record.Attempt)
		if err != nil || !r.remindDue(cur, now) {
			return err
		}
		cur.Reminders++
		if err := enqueueHeldNotice(ctx, tx, cur.BuildOutage); err != nil {
			return err
		}
		_, err = handoff.PutBuildOutage(ctx, tx, cur.BuildOutage, cur.version)
		return err
	})
	return err == nil, err
}

// enqueueHeldNotice queues the Owner's notice of a held build, once per
// hold, or the reminder numbered o.Reminders when it is not zero.
func enqueueHeldNotice(ctx context.Context, tx state.Tx, o BuildOutage) error {
	raw, err := json.Marshal(handoff.BuildHeldNotice{IssueID: o.IssueID, RunID: o.RunID, Attempt: o.Attempt,
		Failures: o.Failures, LastCode: o.LastCode, Reason: o.Reason, Holds: o.Holds, Reminder: o.Reminders, HeldAt: o.HeldAt})
	if err != nil {
		return core.NewError(core.CodeInternal, "a held build notice cannot be encoded").WithCause(err)
	}
	key := handoff.BuildOutageKey(o.RunID, o.Attempt) + "/held/" + itoa(o.Holds)
	if o.Reminders > 0 {
		key += "/reminder/" + itoa(o.Reminders)
	}
	return tx.Enqueue(ctx, state.OutboxMessage{Topic: handoff.TopicBuildHeld, Payload: raw, Key: key})
}

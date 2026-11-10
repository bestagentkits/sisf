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
}

// DefaultBuildRetry waits 30 seconds after the first outage, at most ten
// minutes between tries, and holds the build after five in a row.
var DefaultBuildRetry = BuildRetry{First: 30 * time.Second, Max: 10 * time.Minute, HoldAfter: 5}

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
	return tx.DeleteKV(ctx, handoff.BuildNamespace, handoff.BuildOutageKey(run, attempt), e.version)
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
		if r.held(next) && !cur.Held {
			next.Held = true
			next.Holds++
			if err := enqueueHeldNotice(ctx, tx, next.BuildOutage); err != nil {
				return err
			}
		}
		next.version, err = handoff.PutBuildOutage(ctx, tx, next.BuildOutage, cur.version)
		return err
	})
	return next, err
}

// enqueueHeldNotice queues the Owner's notice of a held build, once per hold.
func enqueueHeldNotice(ctx context.Context, tx state.Tx, o BuildOutage) error {
	raw, err := json.Marshal(handoff.BuildHeldNotice{IssueID: o.IssueID, RunID: o.RunID, Attempt: o.Attempt,
		Failures: o.Failures, LastCode: o.LastCode, Holds: o.Holds})
	if err != nil {
		return core.NewError(core.CodeInternal, "a held build notice cannot be encoded").WithCause(err)
	}
	return tx.Enqueue(ctx, state.OutboxMessage{Topic: handoff.TopicBuildHeld, Payload: raw,
		Key: handoff.BuildOutageKey(o.RunID, o.Attempt) + "/held/" + itoa(o.Holds)})
}

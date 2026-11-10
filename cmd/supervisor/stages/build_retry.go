package stages

import (
	"context"
	"encoding/json"
	"fmt"
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
	// HoldAfter is the number of failures in a row after which the patch
	// is reported held.
	HoldAfter int
}

// DefaultBuildRetry waits 30 seconds after the first outage, at most ten
// minutes between tries, and reports the patch held after five in a row.
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

// BuildOutage is the supervisor's record of the outages a patch's build
// met, kept in the build namespace until the patch is built or refused.
type BuildOutage struct {
	RunID    core.RunID `json:"run_id"`
	Attempt  int        `json:"attempt"`
	Failures int        `json:"failures"`
	// LastCode is the code of the last failure; the message is not kept.
	LastCode  core.ErrorCode `json:"last_code"`
	NextTryAt time.Time      `json:"next_try_at"`
	// Held is set once the failures reach the hold threshold.
	Held      bool      `json:"held"`
	UpdatedAt time.Time `json:"updated_at"`
}

// retryEntry is a stored outage record and its version; zero when none.
type retryEntry struct {
	BuildOutage
	version int64
}

// waiting reports whether the backoff of the last outage has not run out.
func (e retryEntry) waiting(now time.Time) bool { return e.version != 0 && now.Before(e.NextTryAt) }

// RetryKey is the key of the outage record of one evaluator attempt.
func RetryKey(run core.RunID, attempt int) string {
	return fmt.Sprintf("build-outage/%s/%d", run, attempt)
}

// ReadBuildOutage reads the outage record of one evaluator attempt,
// NOT_FOUND while its build met none.
func ReadBuildOutage(ctx context.Context, kv state.KV, run core.RunID, attempt int) (BuildOutage, error) {
	e, err := readRetry(ctx, kv, run, attempt)
	if err == nil && e.version == 0 {
		err = core.NewError(core.CodeNotFound, "the build met no outage")
	}
	return e.BuildOutage, err
}

func readRetry(ctx context.Context, kv state.KV, run core.RunID, attempt int) (retryEntry, error) {
	entry, err := kv.GetKV(ctx, handoff.BuildNamespace, RetryKey(run, attempt))
	if core.HasCode(err, core.CodeNotFound) {
		return retryEntry{}, nil
	}
	if err != nil {
		return retryEntry{}, err
	}
	var out BuildOutage
	if json.Unmarshal(entry.Value, &out) != nil || out.RunID != run || out.Attempt != attempt {
		return retryEntry{}, core.NewError(core.CodeTampered, "the stored build outage record cannot be read")
	}
	return retryEntry{BuildOutage: out, version: entry.Version}, nil
}

func clearRetry(ctx context.Context, tx state.KV, run core.RunID, attempt int) error {
	e, err := readRetry(ctx, tx, run, attempt)
	if err != nil || e.version == 0 {
		return err
	}
	return tx.DeleteKV(ctx, handoff.BuildNamespace, RetryKey(run, attempt), e.version)
}

// recordOutage counts one more outage of a patch's build and sets when it
// is tried next. It returns the record written.
func (s *BuildStage) recordOutage(ctx context.Context, j buildJob, cause error, r BuildRetry) (retryEntry, error) {
	var next retryEntry
	err := s.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		cur, err := readRetry(ctx, tx, j.record.RunID, j.record.Attempt)
		if err != nil {
			return err
		}
		now := s.Clock.Now().UTC()
		next = cur
		next.RunID, next.Attempt = j.record.RunID, j.record.Attempt
		next.Failures++
		next.LastCode, next.UpdatedAt = core.CodeOf(cause), now
		next.NextTryAt = now.Add(r.backoff(next.Failures))
		next.Held = r.held(next)
		raw, err := json.Marshal(next.BuildOutage)
		if err != nil {
			return core.NewError(core.CodeInternal, "a build outage record cannot be encoded").WithCause(err)
		}
		written, err := tx.PutKV(ctx, state.KVEntry{Namespace: handoff.BuildNamespace, Key: RetryKey(next.RunID, next.Attempt), Value: raw, Version: cur.version})
		next.version = written.Version
		return err
	})
	return next, err
}

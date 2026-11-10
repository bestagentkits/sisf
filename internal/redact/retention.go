package redact

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// DataKind is a kind of stored operational data with its own retention
// period. The audit log is not one: it is append-only and never purged.
type DataKind string

const (
	KindLog        DataKind = "log"
	KindMetric     DataKind = "metric"
	KindEvent      DataKind = "event"
	KindTranscript DataKind = "transcript"
	// KindReport is content submitted through the public report endpoint.
	KindReport DataKind = "report"
)

var allKinds = []DataKind{KindLog, KindMetric, KindEvent, KindTranscript, KindReport}

// DataKinds returns every kind that needs a retention period. The caller
// owns the returned slice.
func DataKinds() []DataKind { return slices.Clone(allKinds) }

const day = 24 * time.Hour

// Retention is how long each kind of data is kept.
type Retention map[DataKind]time.Duration

// DefaultRetention keeps logs and transcripts, which are the most likely to
// hold personal data, for 30 days and aggregate signals for 90 days.
func DefaultRetention() Retention {
	return Retention{
		KindLog:        30 * day,
		KindMetric:     90 * day,
		KindEvent:      90 * day,
		KindTranscript: 30 * day,
		KindReport:     90 * day,
	}
}

// Validate requires a positive period for every kind and no unknown kind.
func (r Retention) Validate() error {
	for _, kind := range allKinds {
		if r[kind] <= 0 {
			return core.NewError(core.CodeInvalidArgument, fmt.Sprintf("retention for %s must be a positive period", kind))
		}
	}
	for kind := range r {
		if !slices.Contains(allKinds, kind) {
			return core.NewError(core.CodeInvalidArgument, fmt.Sprintf("retention names an unknown kind %q", kind))
		}
	}
	return nil
}

// Expired reports whether data of kind stored at storedAt is past its
// period at now. A kind without a period is treated as expired, so a gap in
// the policy deletes rather than keeps.
func (r Retention) Expired(kind DataKind, storedAt, now time.Time) bool {
	period, ok := r[kind]
	return !ok || period <= 0 || !storedAt.After(now.Add(-period))
}

// PurgeFunc deletes the data of one kind stored before the cutoff and
// returns how many records it removed.
type PurgeFunc func(ctx context.Context, before time.Time) (int, error)

// Sweeper applies a retention policy to the stores that hold each kind.
type Sweeper struct {
	policy  Retention
	clock   core.Clock
	purgers map[DataKind]PurgeFunc
}

// NewSweeper checks the policy is complete and every purger belongs to a
// kind that has a period.
func NewSweeper(policy Retention, clock core.Clock, purgers map[DataKind]PurgeFunc) (*Sweeper, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if clock == nil {
		clock = core.SystemClock{}
	}
	for kind, purge := range purgers {
		if _, ok := policy[kind]; !ok || purge == nil {
			return nil, core.NewError(core.CodeInvalidArgument, fmt.Sprintf("no retention period or purger for %q", kind))
		}
	}
	return &Sweeper{policy: policy, clock: clock, purgers: purgers}, nil
}

// Sweep purges every kind once and returns how many records each purge
// removed. Every kind is attempted even when one fails; the first failure
// is returned.
func (s *Sweeper) Sweep(ctx context.Context) (map[DataKind]int, error) {
	now := s.clock.Now()
	removed := map[DataKind]int{}
	var first error
	for _, kind := range allKinds {
		purge, ok := s.purgers[kind]
		if !ok {
			continue
		}
		n, err := purge(ctx, now.Add(-s.policy[kind]))
		if err != nil && first == nil {
			first = core.NewError(core.CodeUnavailable, fmt.Sprintf("purging %s failed", kind)).WithCause(err)
		}
		removed[kind] = n
	}
	return removed, first
}

package telemetry

import (
	"context"
	"maps"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// EventStore keeps normalized events so they can be read back. What it
// returns is untrusted data to whoever reads it.
type EventStore interface {
	Append(ctx context.Context, e Event) error
	Reader
}

// DefaultStoreCapacity bounds a MemoryStore built with a capacity below 1.
const DefaultStoreCapacity = 10000

// MemoryStore is a bounded in-process EventStore. When full it drops the
// oldest events, so a burst of signals cannot exhaust memory. It is safe for
// concurrent use.
type MemoryStore struct {
	mu       sync.Mutex
	capacity int
	events   []Event
}

var _ EventStore = (*MemoryStore)(nil)

// NewMemoryStore returns a store that keeps at most capacity events.
func NewMemoryStore(capacity int) *MemoryStore {
	if capacity < 1 {
		capacity = DefaultStoreCapacity
	}
	return &MemoryStore{capacity: capacity}
}

// Append keeps a copy of e.
func (s *MemoryStore) Append(_ context.Context, e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) >= s.capacity {
		// Drop the oldest tenth at once, so the shift is amortized instead
		// of paid on every append once the store is full.
		drop := max(1, s.capacity/10)
		n := copy(s.events, s.events[drop:])
		clear(s.events[n:])
		s.events = s.events[:n]
	}
	s.events = append(s.events, clone(e))
	return nil
}

// Query returns copies of the matching events in the order they were
// appended, at most q.Limit of them when it is set. The time range is
// half-open: Since is included and Until is not, so adjacent windows never
// count an event twice.
func (s *MemoryStore) Query(_ context.Context, q Query) ([]Event, error) {
	if q.Limit < 0 {
		return nil, core.NewError(core.CodeInvalidArgument, "a query limit must not be negative")
	}
	if q.Kind != "" && !q.Kind.Valid() {
		return nil, core.NewError(core.CodeInvalidArgument, "a query kind must be log, metric or event")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for _, e := range s.events {
		if q.Limit > 0 && len(out) == q.Limit {
			break
		}
		if matches(q, e) {
			out = append(out, clone(e))
		}
	}
	return out, nil
}

// DeleteBefore removes the events of kind recorded before cutoff and
// returns how many it removed. It is how a retention period is applied.
func (s *MemoryStore) DeleteBefore(kind Kind, cutoff time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.events[:0]
	for _, e := range s.events {
		if e.Kind != kind || !e.At.Before(cutoff) {
			kept = append(kept, e)
		}
	}
	removed := len(s.events) - len(kept)
	clear(s.events[len(kept):])
	s.events = kept
	return removed
}

func matches(q Query, e Event) bool {
	a := e.Attributes
	switch {
	case q.Kind != "" && e.Kind != q.Kind,
		q.Name != "" && e.Name != q.Name,
		q.ReleaseDigest != "" && a.ReleaseDigest != q.ReleaseDigest,
		q.RunID != "" && a.RunID != q.RunID,
		!q.IssueRef.IsZero() && a.IssueRef != q.IssueRef,
		!q.Since.IsZero() && e.At.Before(q.Since),
		!q.Until.IsZero() && !e.At.Before(q.Until):
		return false
	}
	return true
}

func clone(e Event) Event {
	e.Fields = maps.Clone(e.Fields)
	return e
}

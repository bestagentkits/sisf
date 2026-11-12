package edge

import (
	"errors"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	// bucketWidth is the resolution of a windowed count; a window is rounded
	// out to whole buckets.
	bucketWidth = 10 * time.Second
	// MaxWindow is the longest window the counters can answer.
	MaxWindow = time.Hour
)

// Counts is what the edge saw for one revision.
type Counts struct {
	// Requests were received for the revision.
	Requests int64 `json:"requests"`
	// Samples are the requests that got a response status. A request the
	// client abandoned before any status is a request and not a sample, so
	// Samples is the denominator of an error rate.
	Samples int64 `json:"samples"`
	// ServerErrors are the samples whose status was 5xx, a 502 the edge
	// itself answered for an unreachable upstream included.
	ServerErrors int64 `json:"server_errors"`
}

func (c *Counts) add(o Counts) {
	c.Requests += o.Requests
	c.Samples += o.Samples
	c.ServerErrors += o.ServerErrors
}

// series is the history of one revision.
type series struct {
	total   Counts
	buckets map[int64]*Counts
}

// Counters keeps per-revision counts, in total and over recent windows, for
// a monitor to read. It is safe for concurrent use.
type Counters struct {
	clock core.Clock
	mu    sync.Mutex
	revs  map[string]*series
}

// NewCounters returns empty counters stamped by clock.
func NewCounters(clock core.Clock) *Counters {
	return &Counters{clock: clock, revs: map[string]*series{}}
}

// record adds delta to revision's total and to the current bucket.
func (c *Counters) record(revision string, delta Counts) {
	now := c.clock.Now()
	key := now.UnixNano() / int64(bucketWidth)
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.revs[revision]
	if s == nil {
		s = &series{buckets: map[int64]*Counts{}}
		c.revs[revision] = s
	}
	s.total.add(delta)
	b := s.buckets[key]
	if b == nil {
		b = &Counts{}
		s.buckets[key] = b
		oldest := key - int64(MaxWindow/bucketWidth)
		for k := range s.buckets {
			if k < oldest {
				delete(s.buckets, k)
			}
		}
	}
	b.add(delta)
}

// Totals returns everything counted since the edge started.
func (c *Counters) Totals() map[string]Counts {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]Counts, len(c.revs))
	for rev, s := range c.revs {
		out[rev] = s.total
	}
	return out
}

// Window returns what was counted over the last d, rounded out to whole
// buckets. A window must be positive and no longer than MaxWindow.
func (c *Counters) Window(d time.Duration) (map[string]Counts, error) {
	if d <= 0 || d > MaxWindow {
		return nil, errors.New("a window must be longer than zero and at most one hour")
	}
	first := c.clock.Now().Add(-d).UnixNano() / int64(bucketWidth)
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]Counts, len(c.revs))
	for rev, s := range c.revs {
		var sum Counts
		for key, b := range s.buckets {
			if key >= first {
				sum.add(*b)
			}
		}
		out[rev] = sum
	}
	return out, nil
}

package egress

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Blocked is one connection the proxy refused. It names the destination and
// the reason, never a path, a query or a header: those may carry what a
// manipulated agent tried to send out.
type Blocked struct {
	At     time.Time `json:"at"`
	Client string    `json:"client"`
	Method string    `json:"method"`
	Host   string    `json:"host"`
	Port   string    `json:"port"`
	Reason string    `json:"reason"`
}

// Destination sums up the blocked attempts to one host, for the Owner to
// review and, if they choose, add through Service.
type Destination struct {
	Host       string    `json:"host"`
	Count      int       `json:"count"`
	LastAt     time.Time `json:"last_at"`
	LastReason string    `json:"last_reason"`
}

// Recorder keeps blocked destinations for the Owner's review.
type Recorder interface {
	RecordBlocked(ctx context.Context, b Blocked)
}

// MemoryRecorder keeps the most recent blocked attempts and a count per
// destination. It is safe for concurrent use.
type MemoryRecorder struct {
	mu     sync.Mutex
	limit  int
	recent []Blocked
	byHost map[string]*Destination
}

// NewMemoryRecorder keeps up to limit recent attempts and as many
// destinations; 1000 when limit is not positive.
func NewMemoryRecorder(limit int) *MemoryRecorder {
	if limit <= 0 {
		limit = 1000
	}
	return &MemoryRecorder{limit: limit, byHost: map[string]*Destination{}}
}

// RecordBlocked keeps one attempt.
func (m *MemoryRecorder) RecordBlocked(_ context.Context, b Blocked) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recent = append(m.recent, b)
	if len(m.recent) > m.limit {
		m.recent = slices.Delete(m.recent, 0, len(m.recent)-m.limit)
	}
	d, ok := m.byHost[b.Host]
	if !ok {
		if len(m.byHost) >= m.limit {
			return // the count of a new host is lost; the attempt is still in recent
		}
		d = &Destination{Host: b.Host}
		m.byHost[b.Host] = d
	}
	d.Count++
	d.LastAt, d.LastReason = b.At, b.Reason
}

// Recent returns the kept attempts, oldest first.
func (m *MemoryRecorder) Recent() []Blocked {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.recent)
}

// Destinations returns the blocked hosts, most often blocked first.
func (m *MemoryRecorder) Destinations() []Destination {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Destination, 0, len(m.byHost))
	for _, d := range m.byHost {
		out = append(out, *d)
	}
	slices.SortFunc(out, func(a, b Destination) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		if a.Host < b.Host {
			return -1
		}
		return 1
	})
	return out
}

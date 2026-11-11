package report

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Report is one submission from the public report endpoint. Its text is
// data: it is stored, shown and quoted, never followed.
type Report struct {
	ID string `json:"id"`
	// Fingerprint is the hash of the normalized, redacted content; equal
	// fingerprints are the same report.
	Fingerprint string    `json:"fingerprint"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	ReceivedAt  time.Time `json:"received_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	// Count is how many times the same report was submitted.
	Count int `json:"count"`
	// Client names who sent the report, for a store that makes room by
	// sender: the handler sets the client key of the request (Config.
	// ClientKey), and a store keeps it only in a form that does not reveal
	// it, or not at all. It is never shown.
	Client string `json:"client,omitempty"`
}

// Store keeps reports, deduplicated by fingerprint.
type Store interface {
	// Save stores r, or counts it against the stored report with the same
	// fingerprint. It returns the stored report and whether it was new.
	Save(ctx context.Context, r Report) (Report, bool, error)
}

// MemoryStore is a bounded in-process Store. It is safe for concurrent use.
type MemoryStore struct {
	mu       sync.Mutex
	capacity int
	byFP     map[string]*Report
	order    []string
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns a store that holds at most capacity distinct
// reports; below 1 means 10000.
func NewMemoryStore(capacity int) *MemoryStore {
	if capacity < 1 {
		capacity = 10000
	}
	return &MemoryStore{capacity: capacity, byFP: map[string]*Report{}}
}

// Save implements Store. A full store refuses new reports rather than
// dropping stored ones; a duplicate is still counted.
func (m *MemoryStore) Save(_ context.Context, r Report) (Report, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if stored, ok := m.byFP[r.Fingerprint]; ok {
		stored.Count++
		stored.LastSeenAt = r.ReceivedAt
		return *stored, false, nil
	}
	if len(m.order) >= m.capacity {
		return Report{}, false, core.NewError(core.CodeLimitExceeded, "the report store is full")
	}
	// The memory store does not make room by sender, so it keeps no client.
	r.Count, r.LastSeenAt, r.Client = 1, r.ReceivedAt, ""
	m.byFP[r.Fingerprint] = &r
	m.order = append(m.order, r.Fingerprint)
	return r, true, nil
}

// All returns copies of the stored reports in the order they arrived.
func (m *MemoryStore) All() []Report {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Report, 0, len(m.order))
	for _, fp := range m.order {
		out = append(out, *m.byFP[fp])
	}
	return out
}

// Purge removes the reports first received before cutoff and returns how
// many it removed. Its signature fits a retention sweeper.
func (m *MemoryStore) Purge(_ context.Context, cutoff time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	before := len(m.order)
	m.order = slices.DeleteFunc(m.order, func(fp string) bool {
		if m.byFP[fp].ReceivedAt.Before(cutoff) {
			delete(m.byFP, fp)
			return true
		}
		return false
	})
	return before - len(m.order), nil
}

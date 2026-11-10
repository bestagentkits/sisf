// Package intaketest holds the in-process fakes the policy, intake and
// approval tests share: an operational store kept in memory, a clock that
// only moves when told to, a signing key and an audit log.
//
// The fakes follow the documented contracts of the interfaces they stand in
// for, so a test that passes against them describes behavior a real backend
// must also show. They are for tests only.
package intaketest

import (
	"context"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// State is a state.Store kept in memory. It models issue records, versioned
// documents and the outbox; the parts it does not model panic when used, so a
// test never passes by reaching something that was not there.
//
// Transactions are serialized: a write transaction works on a copy that
// replaces the stored data only when the callback returns nil.
type State struct {
	mu        sync.Mutex
	clock     core.Clock
	data      tables
	conflicts int
}

// tables is everything one transaction can see.
type tables struct {
	records map[string]core.StateRecord
	// writes remembers the record each idempotency key produced.
	writes map[string]core.StateRecord
	kv     map[string]state.KVEntry
	outbox []state.OutboxMessage
}

func (t tables) clone() tables {
	return tables{
		records: maps.Clone(t.records),
		writes:  maps.Clone(t.writes),
		kv:      maps.Clone(t.kv),
		outbox:  slices.Clone(t.outbox),
	}
}

// NewState returns an empty store that stamps its writes from clock.
func NewState(clock core.Clock) *State {
	return &State{clock: clock, data: tables{
		records: map[string]core.StateRecord{},
		writes:  map[string]core.StateRecord{},
		kv:      map[string]state.KVEntry{},
	}}
}

// Update runs fn in a read-write transaction that commits only on nil.
func (s *State) Update(ctx context.Context, fn func(ctx context.Context, tx state.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	work := s.data.clone()
	if err := fn(ctx, &tx{t: &work, now: s.clock.Now(), owner: s}); err != nil {
		return err
	}
	s.data = work
	return nil
}

// View runs fn against a copy that is thrown away afterwards.
func (s *State) View(ctx context.Context, fn func(ctx context.Context, tx state.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	work := s.data.clone()
	return fn(ctx, &tx{t: &work, now: s.clock.Now(), owner: s})
}

// Close does nothing.
func (s *State) Close() error { return nil }

// ConflictNextWrites makes the next n document writes lose their
// compare-and-swap, as a concurrent writer would cause.
func (s *State) ConflictNextWrites(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conflicts = n
}

// Record returns the stored record of issue and whether it exists.
func (s *State) Record(issue core.IssueRef) (core.StateRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.data.records[issue.String()]
	return record, ok
}

// Outbox returns every queued message, sent or not, in the order queued.
func (s *State) Outbox() []state.OutboxMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.data.outbox)
}

// tx is one transaction over a private copy of the tables.
type tx struct {
	// The embedded interface is nil: leases, the inbox and the budget ledger
	// are not modeled and panic when called.
	state.Tx
	t     *tables
	now   time.Time
	owner *State
}

func conflict(message string) error { return core.NewError(core.CodeConflict, message) }

func notFound(message string) error { return core.NewError(core.CodeNotFound, message) }

func (x *tx) GetRecord(_ context.Context, issue core.IssueRef) (core.StateRecord, error) {
	record, ok := x.t.records[issue.String()]
	if !ok {
		return core.StateRecord{}, notFound("the issue has no record")
	}
	return record, nil
}

func (x *tx) ListRecords(_ context.Context, filter state.RecordFilter) ([]core.StateRecord, error) {
	var out []core.StateRecord
	for _, record := range x.t.records {
		if len(filter.States) > 0 && !slices.Contains(filter.States, record.State) {
			continue
		}
		if !filter.UpdatedSince.IsZero() && record.UpdatedAt.Before(filter.UpdatedSince) {
			continue
		}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IssueID.String() < out[j].IssueID.String() })
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

// Transition applies the rules of state.Records: the step must be an edge of
// the lifecycle, and the record must be as the writer read it. Fencing tokens
// are not modeled; intake writes are unleased.
func (x *tx) Transition(_ context.Context, t core.Transition) (core.StateRecord, error) {
	if err := statemachine.CheckEdge(t); err != nil {
		return core.StateRecord{}, err
	}
	if err := t.Validate(); err != nil {
		return core.StateRecord{}, err
	}
	if written, ok := x.t.writes[t.IdempotencyKey]; ok {
		return written, nil
	}
	key := t.IssueID.String()
	record, exists := x.t.records[key]
	switch {
	case t.Creates() && exists:
		return core.StateRecord{}, conflict("the issue already has a record")
	case !t.Creates() && (!exists || record.Version != t.ExpectedVersion || record.State != t.From):
		return core.StateRecord{}, conflict("the record changed since it was read")
	}
	record.IssueID, record.State = t.IssueID, t.To
	record.Version++
	record.IdempotencyKey, record.Attempt, record.UpdatedAt = t.IdempotencyKey, t.Attempt, x.now
	if t.RunID != "" {
		record.RunID = t.RunID
	}
	x.t.records[key] = record
	x.t.writes[t.IdempotencyKey] = record
	return record, nil
}

func kvKey(namespace, key string) string { return namespace + "\x00" + key }

func (x *tx) GetKV(_ context.Context, namespace, key string) (state.KVEntry, error) {
	entry, ok := x.t.kv[kvKey(namespace, key)]
	if !ok {
		return state.KVEntry{}, notFound("no such document")
	}
	entry.Value = slices.Clone(entry.Value)
	return entry, nil
}

func (x *tx) PutKV(_ context.Context, e state.KVEntry) (state.KVEntry, error) {
	if x.owner.conflicts > 0 {
		x.owner.conflicts--
		return state.KVEntry{}, conflict("another writer changed the document")
	}
	stored := x.t.kv[kvKey(e.Namespace, e.Key)]
	if stored.Version != e.Version {
		return state.KVEntry{}, conflict("the document changed since it was read")
	}
	e.Version++
	e.Value, e.UpdatedAt = slices.Clone(e.Value), x.now
	x.t.kv[kvKey(e.Namespace, e.Key)] = e
	return e, nil
}

func (x *tx) ListKV(_ context.Context, namespace, prefix string, limit int) ([]state.KVEntry, error) {
	var out []state.KVEntry
	for _, entry := range x.t.kv {
		if entry.Namespace == namespace && strings.HasPrefix(entry.Key, prefix) {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (x *tx) DeleteKV(_ context.Context, namespace, key string, version int64) error {
	if x.t.kv[kvKey(namespace, key)].Version != version {
		return conflict("the document changed since it was read")
	}
	delete(x.t.kv, kvKey(namespace, key))
	return nil
}

// Enqueue keeps one message per key, as state.Outbox requires.
func (x *tx) Enqueue(_ context.Context, m state.OutboxMessage) error {
	if slices.ContainsFunc(x.t.outbox, func(queued state.OutboxMessage) bool { return queued.Key == m.Key }) {
		return nil
	}
	m.ID, m.CreatedAt = int64(len(x.t.outbox)+1), x.now
	m.Payload = slices.Clone(m.Payload)
	x.t.outbox = append(x.t.outbox, m)
	return nil
}

func (x *tx) PendingOutbox(_ context.Context, limit int) ([]state.OutboxMessage, error) {
	var out []state.OutboxMessage
	for _, m := range x.t.outbox {
		if m.SentAt.IsZero() && !m.NextAttemptAt.After(x.now) && (limit <= 0 || len(out) < limit) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (x *tx) MarkOutboxSent(_ context.Context, id int64) error {
	return x.editOutbox(id, func(m *state.OutboxMessage) { m.SentAt = x.now })
}

func (x *tx) MarkOutboxFailed(_ context.Context, id int64, retryAt time.Time, reason string) error {
	return x.editOutbox(id, func(m *state.OutboxMessage) {
		m.Attempts++
		m.NextAttemptAt, m.LastError = retryAt, reason
	})
}

func (x *tx) editOutbox(id int64, edit func(*state.OutboxMessage)) error {
	for i := range x.t.outbox {
		if x.t.outbox[i].ID == id {
			edit(&x.t.outbox[i])
			return nil
		}
	}
	return notFound("no such outbox message")
}

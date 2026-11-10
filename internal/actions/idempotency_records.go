package actions

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// IdempotencyNamespace is the namespace of the control zone's store that
// holds idempotency records. No other zone reads or writes it.
const IdempotencyNamespace = "control.idempotency"

// Record keys start with the UTC day the record was made, so the oldest
// sort first and lapsed ones are pruned from the front.
const (
	idempotencyDayLayout = "20060102"
	// idempotencyPruneBatch bounds how many lapsed records one reservation
	// removes.
	idempotencyPruneBatch = 64
	// idempotencyReserveAttempts bounds the retries of a reservation that
	// lost a race with another process.
	idempotencyReserveAttempts = 2
)

// NewKVIdempotency keeps idempotency records in store, in
// IdempotencyNamespace. A record lives in the bucket of the day it was
// made; each reservation prunes the buckets older than the retention.
func NewKVIdempotency(store state.Store) IdempotencyRecords { return kvIdempotency{store: store} }

type kvIdempotency struct{ store state.Store }

func dayBucket(t time.Time) string { return t.UTC().Format(idempotencyDayLayout) + "/" }

// Reserve looks for a live record of key in the buckets of the retention
// and stores rec in today's when there is none.
func (k kvIdempotency) Reserve(ctx context.Context, key string, rec IdempotencyRecord) (IdempotencyRecord, bool, error) {
	var held IdempotencyRecord
	var reserved bool
	var err error
	for range idempotencyReserveAttempts {
		err = k.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			found, ok, err := liveRecord(ctx, tx, key, rec.CreatedAt)
			if err != nil || ok {
				held, reserved = found, false
				return err
			}
			if err := pruneLapsed(ctx, tx, rec.CreatedAt); err != nil {
				return err
			}
			reserved = true
			return putRecord(ctx, tx, dayBucket(rec.CreatedAt)+key, 0, rec)
		})
		if !core.HasCode(err, core.CodeConflict) {
			break
		}
	}
	return held, reserved, err
}

// Complete stores the answer in the record the reservation made.
func (k kvIdempotency) Complete(ctx context.Context, key string, rec IdempotencyRecord) error {
	return k.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, IdempotencyNamespace, dayBucket(rec.CreatedAt)+key)
		if err != nil {
			return err
		}
		return putRecord(ctx, tx, entry.Key, entry.Version, rec)
	})
}

// Release removes the record the reservation made.
func (k kvIdempotency) Release(ctx context.Context, key string, reserved time.Time) error {
	return k.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, IdempotencyNamespace, dayBucket(reserved)+key)
		if core.HasCode(err, core.CodeNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return tx.DeleteKV(ctx, IdempotencyNamespace, entry.Key, entry.Version)
	})
}

// liveRecord finds the record of key that is still live at now. A live
// record was made today or yesterday.
func liveRecord(ctx context.Context, tx state.Tx, key string, now time.Time) (IdempotencyRecord, bool, error) {
	for _, day := range []time.Time{now, now.Add(-IdempotencyRetention)} {
		entry, err := tx.GetKV(ctx, IdempotencyNamespace, dayBucket(day)+key)
		if core.HasCode(err, core.CodeNotFound) {
			continue
		}
		if err != nil {
			return IdempotencyRecord{}, false, err
		}
		var rec IdempotencyRecord
		if err := json.Unmarshal(entry.Value, &rec); err != nil {
			return IdempotencyRecord{}, false, core.NewError(core.CodeTampered, "a stored idempotency record cannot be read").WithCause(err)
		}
		if rec.live(now) {
			return rec, true, nil
		}
	}
	return IdempotencyRecord{}, false, nil
}

// pruneLapsed removes records from buckets older than the retention, the
// oldest first, at most idempotencyPruneBatch of them.
func pruneLapsed(ctx context.Context, tx state.Tx, now time.Time) error {
	oldest := dayBucket(now.Add(-IdempotencyRetention))
	entries, err := tx.ListKV(ctx, IdempotencyNamespace, "", idempotencyPruneBatch)
	if err != nil {
		return err
	}
	for _, e := range entries {
		day, _, ok := strings.Cut(e.Key, "/")
		if !ok || day+"/" >= oldest {
			break
		}
		if err := tx.DeleteKV(ctx, IdempotencyNamespace, e.Key, e.Version); err != nil {
			return err
		}
	}
	return nil
}

func putRecord(ctx context.Context, tx state.Tx, key string, version int64, rec IdempotencyRecord) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return core.NewError(core.CodeInternal, "an idempotency record cannot be encoded").WithCause(err)
	}
	_, err = tx.PutKV(ctx, state.KVEntry{Namespace: IdempotencyNamespace, Key: key, Value: raw, Version: version})
	return err
}

// memoryIdempotency keeps idempotency records in this process, for a
// service built without a store. Each reservation drops lapsed records.
type memoryIdempotency struct {
	mu      sync.Mutex
	records map[string]IdempotencyRecord
}

func (m *memoryIdempotency) Reserve(_ context.Context, key string, rec IdempotencyRecord) (IdempotencyRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.records == nil {
		m.records = map[string]IdempotencyRecord{}
	}
	if held, ok := m.records[key]; ok && held.live(rec.CreatedAt) {
		return held, false, nil
	}
	for k, r := range m.records {
		if !r.live(rec.CreatedAt) {
			delete(m.records, k)
		}
	}
	m.records[key] = rec
	return IdempotencyRecord{}, true, nil
}

func (m *memoryIdempotency) Complete(_ context.Context, key string, rec IdempotencyRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if held, ok := m.records[key]; ok && held.CreatedAt.Equal(rec.CreatedAt) {
		m.records[key] = rec
	}
	return nil
}

func (m *memoryIdempotency) Release(_ context.Context, key string, reserved time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if held, ok := m.records[key]; ok && held.CreatedAt.Equal(reserved) {
		delete(m.records, key)
	}
	return nil
}

package actions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
	"unicode"

	"github.com/bestagentkits/sisf/pkg/core"
)

// IdempotencyRetention is how long the answer to a write called with an
// idempotency key is kept: a retry within it gets that answer back without
// the write running again. After it the key may be used afresh.
const IdempotencyRetention = 24 * time.Hour

// maxIdempotencyKeyLen bounds an idempotency key.
const maxIdempotencyKeyLen = 256

// IdempotencyRecord is what is kept for one (caller, action, key): the
// digest of the call that first used the key and, once that call
// succeeded, its answer.
type IdempotencyRecord struct {
	CallDigest core.Digest `json:"call_digest"`
	CreatedAt  time.Time   `json:"created_at"`
	// Done is set when the first call succeeded; Result is its answer.
	Done   bool              `json:"done"`
	Result core.ActionResult `json:"result"`
}

// live reports whether the record still holds its key at now.
func (r IdempotencyRecord) live(now time.Time) bool {
	return now.Sub(r.CreatedAt) < IdempotencyRetention
}

// IdempotencyRecords keeps idempotency records by key. NewKVIdempotency
// keeps them in the control zone's store.
type IdempotencyRecords interface {
	// Reserve stores rec under key and reports true, unless a record that
	// is still live at rec.CreatedAt holds the key: then it returns that
	// record and false, and stores nothing.
	Reserve(ctx context.Context, key string, rec IdempotencyRecord) (IdempotencyRecord, bool, error)
	// Complete replaces the record the reserving call stored under key with
	// rec, which carries its answer.
	Complete(ctx context.Context, key string, rec IdempotencyRecord) error
	// Release removes the record stored at reserved under key, so a retry
	// of a call that failed runs again.
	Release(ctx context.Context, key string, reserved time.Time) error
}

// validIdempotencyKey refuses a key that is too long or holds a character
// that is not printable.
func validIdempotencyKey(key string) error {
	if len(key) > maxIdempotencyKeyLen {
		return core.NewError(core.CodeInvalidArgument, "the idempotency key is longer than 256 bytes")
	}
	for _, r := range key {
		if !unicode.IsPrint(r) {
			return core.NewError(core.CodeInvalidArgument, "the idempotency key holds a character that is not printable")
		}
	}
	return nil
}

// idempotent reports whether the call is a write whose key must make it
// apply once: a dry run, a read and a fallback change nothing.
func (s *Service) idempotent(call invocation) bool {
	return call.req.IdempotencyKey != "" && call.spec.Mutates() && !call.req.DryRun &&
		!s.cfg.Registry.IsFallback(call.spec.Name)
}

// once runs the call the first time its caller uses its key for its action
// and answers every later call with the same key with the first answer,
// without running it again. The same key with another input or approval is
// CONFLICT. Concurrent calls with one key run it once: in this process
// they wait for each other, and a call that finds the key held by one that
// has not finished, in another process or one that stopped, is CONFLICT.
// A call that fails frees the key, so a retry runs again.
func (s *Service) once(ctx context.Context, call invocation, run func() (core.ActionResult, error)) (core.ActionResult, error) {
	key := idempotencyRecordKey(call)
	digest := idempotencyCallDigest(call)
	unlock := s.keys.lock(key)
	defer unlock()
	now := s.cfg.Clock.Now()
	held, reserved, err := s.cfg.Idempotency.Reserve(ctx, key, IdempotencyRecord{CallDigest: digest, CreatedAt: now})
	if err != nil {
		return core.ActionResult{}, unavailable("the idempotency key could not be checked", err)
	}
	if !reserved {
		return replay(call, held, digest)
	}
	// Recording the outcome must not be lost with the caller's context: a
	// record left behind would hold the key until it lapses.
	keep := context.WithoutCancel(ctx)
	res, err := run()
	if err != nil {
		// The call stays refused or failed either way; a key that could not
		// be freed only makes a retry answer CONFLICT until it lapses.
		_ = s.cfg.Idempotency.Release(keep, key, now)
		return res, err
	}
	// The write happened. If its answer cannot be kept, the record stays
	// unfinished and a retry is CONFLICT rather than a second write.
	_ = s.cfg.Idempotency.Complete(keep, key, IdempotencyRecord{CallDigest: digest, CreatedAt: now, Done: true, Result: res})
	return res, nil
}

// replay answers a call whose key is held by an earlier call.
func replay(call invocation, held IdempotencyRecord, digest core.Digest) (core.ActionResult, error) {
	switch {
	case held.CallDigest != digest:
		e := core.NewError(core.CodeConflict, "this idempotency key was already used for "+call.spec.Name+
			" with another input or approval; use a new key for a different call")
		e.Details = map[string]string{"reason": "idempotency_key_reused"}
		return core.ActionResult{}, e
	case !held.Done:
		e := core.NewError(core.CodeConflict, "a call to "+call.spec.Name+" with this idempotency key has not finished;"+
			" check its effect before trying again with a new key")
		e.Details = map[string]string{"reason": "idempotency_key_in_flight"}
		return core.ActionResult{}, e
	}
	return held.Result, nil
}

// idempotencyRecordKey names the record of a key: one per caller, action
// and key, so one caller's key never answers another's call.
func idempotencyRecordKey(call invocation) string {
	payload := core.NewPayloadBuilder("sisf.idempotency.key", 1).
		Text("actor", call.req.Actor.ID).
		Text("action", call.spec.Name).
		Text("key", call.req.IdempotencyKey).
		Bytes()
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// idempotencyCallDigest is what a retry must repeat to get the first
// answer: the canonical input and the approval it spends.
func idempotencyCallDigest(call invocation) core.Digest {
	payload := core.NewPayloadBuilder("sisf.idempotency.call", 1).
		Text("input", string(call.req.Input)).
		Text("approval_id", call.req.ApprovalID).
		Bytes()
	sum := sha256.Sum256(payload)
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// unavailable wraps a failure of the store as UNAVAILABLE unless it is
// already typed.
func unavailable(message string, err error) error {
	if core.CodeOf(err) != core.CodeInternal {
		return err
	}
	return core.NewError(core.CodeUnavailable, message).WithCause(err)
}

// keyedLocks serializes the calls of one record key within this process.
type keyedLocks struct {
	mu   sync.Mutex
	held map[string]*keyLock
}

type keyLock struct {
	mu    sync.Mutex
	users int
}

// lock waits for key and returns the function that frees it.
func (k *keyedLocks) lock(key string) func() {
	k.mu.Lock()
	if k.held == nil {
		k.held = map[string]*keyLock{}
	}
	l, ok := k.held[key]
	if !ok {
		l = &keyLock{}
		k.held[key] = l
	}
	l.users++
	k.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		if l.users--; l.users == 0 {
			delete(k.held, key)
		}
		k.mu.Unlock()
	}
}

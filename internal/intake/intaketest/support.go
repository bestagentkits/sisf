package intaketest

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"slices"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Clock is a core.Clock that moves only when a test advances it, so expiry
// and timeout rules are driven to the exact instant without sleeping.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock standing at now.
func NewClock(now time.Time) *Clock { return &Clock{now: now} }

// Now returns the instant the clock stands at.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After delivers at once: nothing in these tests waits on real time.
func (c *Clock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- c.Now().Add(d)
	return ch
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Keys is an ed25519 key pair that signs and verifies, standing in for the
// delegated signing key of the approval service. The key is derived from its
// name, so tests are repeatable and no key material is stored anywhere.
type Keys struct {
	mu      sync.Mutex
	id      string
	private ed25519.PrivateKey
	revoked bool
}

// NewKeys derives the key pair named id.
func NewKeys(id string) *Keys {
	seed := sha256.Sum256([]byte("intaketest key " + id))
	return &Keys{id: id, private: ed25519.NewKeyFromSeed(seed[:])}
}

// KeyID names the key.
func (k *Keys) KeyID() string { return k.id }

// Sign signs payload with the key.
func (k *Keys) Sign(_ context.Context, payload []byte) (core.Signature, error) {
	return core.Signature{KeyID: k.id, Algorithm: "ed25519", Value: ed25519.Sign(k.private, payload)}, nil
}

// Revoke makes every signature of the key fail verification from now on.
func (k *Keys) Revoke() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.revoked = true
}

// Verify accepts only an unrevoked signature of this key over payload.
func (k *Keys) Verify(_ context.Context, payload []byte, sig core.Signature) error {
	k.mu.Lock()
	revoked := k.revoked
	k.mu.Unlock()
	public, _ := k.private.Public().(ed25519.PublicKey)
	if revoked || sig.KeyID != k.id || sig.Algorithm != "ed25519" || !ed25519.Verify(public, payload, sig.Value) {
		return core.NewError(core.CodeSignatureInvalid, "the signature does not verify")
	}
	return nil
}

// AuditLog is a core.AuditLog that keeps its records in memory.
type AuditLog struct {
	mu     sync.Mutex
	events []core.AuditEvent
	fail   error
	// spare is how many appends still succeed before fail applies.
	spare int
}

// FailWith makes every later Append return err; nil restores it.
func (l *AuditLog) FailWith(err error) { l.FailAfter(0, err) }

// FailAfter lets the next n appends succeed and makes every later one
// return err, as a log that goes down in the middle of an operation does.
func (l *AuditLog) FailAfter(n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fail, l.spare = err, n
}

// Append stores the event at the end of the log.
func (l *AuditLog) Append(_ context.Context, e core.AuditEvent) (core.AuditEvent, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail != nil && l.spare == 0 {
		return core.AuditEvent{}, l.fail
	}
	if l.spare > 0 {
		l.spare--
	}
	e.Seq = int64(len(l.events) + 1)
	l.events = append(l.events, e)
	return e, nil
}

// Verify always passes: the fake keeps no hash chain.
func (l *AuditLog) Verify(context.Context) error { return nil }

// Events returns the records appended so far, in order.
func (l *AuditLog) Events() []core.AuditEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// Find returns the records of one action with one outcome.
func (l *AuditLog) Find(action string, outcome core.AuditOutcome) []core.AuditEvent {
	var out []core.AuditEvent
	for _, e := range l.Events() {
		if e.Action == action && e.Outcome == outcome {
			out = append(out, e)
		}
	}
	return out
}

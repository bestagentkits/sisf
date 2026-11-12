package delegation

import (
	"crypto/subtle"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// MaxWebhookOverlap bounds how long a replaced webhook secret is still
// accepted, so a rotation always ends.
const MaxWebhookOverlap = 7 * 24 * time.Hour

// WebhookSecrets is the set of webhook secrets a receiver accepts. Rotating
// adds a new current secret and keeps the one it replaces for an overlap
// window, so deliveries GitHub signed before it switched still verify; once
// the window closes the old secret is refused. Its Secrets method is the
// secret source of github.ReceiverConfig. It is safe for concurrent use.
type WebhookSecrets struct {
	clock core.Clock

	mu      sync.Mutex
	current core.SecretValue
	retired []retiringSecret
}

// retiringSecret is a replaced secret and the instant it stops being accepted.
type retiringSecret struct {
	value core.SecretValue
	until time.Time
}

// NewWebhookSecrets starts with one current secret.
func NewWebhookSecrets(clock core.Clock, current core.SecretValue) (*WebhookSecrets, error) {
	if clock == nil {
		return nil, invalid("webhook secrets need a clock")
	}
	if current.IsZero() {
		return nil, invalid("webhook secrets need a non-empty current secret")
	}
	return &WebhookSecrets{clock: clock, current: current}, nil
}

// Rotate makes next the current secret and keeps the previous one accepted
// for overlap, which must be positive and at most MaxWebhookOverlap. A
// secret that is already accepted cannot be rotated in again.
func (w *WebhookSecrets) Rotate(next core.SecretValue, overlap time.Duration) error {
	if next.IsZero() {
		return invalid("the new webhook secret is empty")
	}
	if overlap <= 0 || overlap > MaxWebhookOverlap {
		return invalid("the overlap window must be positive and at most %s", MaxWebhookOverlap)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.clock.Now()
	w.prune(now)
	candidate := next.Reveal()
	defer clear(candidate)
	for _, active := range w.activeLocked() {
		value := active.Reveal()
		same := subtle.ConstantTimeCompare(value, candidate) == 1
		clear(value)
		if same {
			return invalid("the new webhook secret is already accepted")
		}
	}
	w.retired = append(w.retired, retiringSecret{value: w.current, until: now.Add(overlap)})
	w.current = next
	return nil
}

// Secrets returns the accepted secrets: the current one first, then every
// replaced one whose overlap window is still open at the clock's now.
func (w *WebhookSecrets) Secrets() []core.SecretValue {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prune(w.clock.Now())
	return w.activeLocked()
}

// activeLocked lists the accepted secrets. The caller holds the lock and
// has pruned.
func (w *WebhookSecrets) activeLocked() []core.SecretValue {
	out := make([]core.SecretValue, 0, len(w.retired)+1)
	out = append(out, w.current)
	for _, r := range w.retired {
		out = append(out, r.value)
	}
	return out
}

// prune drops the replaced secrets whose window has closed at now.
func (w *WebhookSecrets) prune(now time.Time) {
	kept := w.retired[:0]
	for _, r := range w.retired {
		if now.Before(r.until) {
			kept = append(kept, r)
		}
	}
	clear(w.retired[len(kept):])
	w.retired = kept
}

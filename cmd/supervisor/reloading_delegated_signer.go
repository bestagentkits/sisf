package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/core"
)

// keyReloadEvery bounds how long a replaced key on the trust volume goes
// unnoticed; keyReloadRetry is how soon a failed reload, or one that found
// no replacement for a key near its end, is tried again.
const (
	keyReloadEvery = time.Hour
	keyReloadRetry = time.Minute
)

// keyLoader reads one purpose's delegated key from the trust volume, issuing
// a replacement first when the supervisor holds the root key. The chain
// verifier trusts the delegation before the signer is returned. It also
// returns when the delegation ends.
type keyLoader func(ctx context.Context) (*delegation.DelegatedSigner, time.Time, error)

// reloadingSigner signs with a delegated key that it replaces while the
// supervisor runs, so a supervisor that outlives a key keeps signing. It
// reads the trust volume again before signing once keyReloadEvery has passed
// or the key is within renewBefore of its end. A reload that fails keeps the
// key in use; that key still refuses to sign once it has ended or is revoked.
type reloadingSigner struct {
	purpose delegation.Purpose
	load    keyLoader
	clock   core.Clock
	log     *slog.Logger

	mu       sync.Mutex
	current  *delegation.DelegatedSigner
	notAfter time.Time
	reloadAt time.Time
}

// newReloadingSigner loads the key of purpose; a key that cannot be loaded
// at the start is an error.
func newReloadingSigner(ctx context.Context, purpose delegation.Purpose, load keyLoader, clock core.Clock, log *slog.Logger) (*reloadingSigner, error) {
	signer, notAfter, err := load(ctx)
	if err != nil {
		return nil, err
	}
	s := &reloadingSigner{purpose: purpose, load: load, clock: clock, log: log, current: signer, notAfter: notAfter}
	s.reloadAt = s.nextReload(clock.Now())
	return s, nil
}

// KeyID identifies the key in use. A signature names the key that made it,
// which is a later one when the key was replaced in between.
func (s *reloadingSigner) KeyID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current.KeyID()
}

// Sign signs payload with the key in use, after replacing it when a reload
// is due.
func (s *reloadingSigner) Sign(ctx context.Context, payload []byte) (core.Signature, error) {
	return s.signer(ctx).Sign(ctx, payload)
}

// signer returns the key in use, reloading it first when that is due.
func (s *reloadingSigner) signer(ctx context.Context) *delegation.DelegatedSigner {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	if now.Before(s.reloadAt) {
		return s.current
	}
	signer, notAfter, err := s.load(ctx)
	if err != nil {
		s.reloadAt = now.Add(keyReloadRetry)
		s.log.Warn("a delegated key could not be reloaded; the key in use is kept", "purpose", string(s.purpose),
			"key", s.current.KeyID(), "ends", s.notAfter.UTC().Format(time.RFC3339), "code", string(core.CodeOf(err)), "error", err.Error())
		return s.current
	}
	if signer.KeyID() != s.current.KeyID() {
		s.log.Info("a delegated key was replaced", "purpose", string(s.purpose), "key", signer.KeyID(),
			"ends", notAfter.UTC().Format(time.RFC3339))
	}
	s.current, s.notAfter = signer, notAfter
	s.reloadAt = s.nextReload(now)
	return s.current
}

// nextReload is when the trust volume is read next: after keyReloadEvery, or
// sooner once the key is within renewBefore of its end, and never sooner
// than keyReloadRetry.
func (s *reloadingSigner) nextReload(now time.Time) time.Time {
	at := now.Add(keyReloadEvery)
	if due := s.notAfter.Add(-renewBefore); due.Before(at) {
		at = due
	}
	if soonest := now.Add(keyReloadRetry); at.Before(soonest) {
		at = soonest
	}
	return at
}

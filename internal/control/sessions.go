package control

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	// DefaultSessionTTL is how long a dashboard login lasts.
	DefaultSessionTTL = 12 * time.Hour
	maxSessions       = 64
)

// sessions are the dashboard logins. A browser proves the Owner token once;
// from then on it holds only a random session ID in an HttpOnly cookie, so the
// token itself is never stored in the browser or sent again. Sessions live in
// memory: restarting the server logs everyone out, which is the safe failure.
type sessions struct {
	mu    sync.Mutex
	clock core.Clock
	ttl   time.Duration
	byID  map[string]time.Time
}

func newSessions(clock core.Clock, ttl time.Duration) *sessions {
	return &sessions{clock: clock, ttl: ttl, byID: map[string]time.Time{}}
}

// create opens a session and returns its ID and expiry. The oldest sessions
// are dropped beyond a small cap, so repeated logins cannot grow memory.
func (s *sessions) create() (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, core.NewError(core.CodeInternal, "a session could not be created").WithCause(err)
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	now := s.clock.Now()
	expires := now.Add(s.ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	s.byID[id] = expires
	return id, expires, nil
}

// valid reports whether id names a live session.
func (s *sessions) valid(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, ok := s.byID[id]
	if ok && !s.clock.Now().Before(expires) {
		delete(s.byID, id)
		return false
	}
	return ok
}

// revoke ends a session.
func (s *sessions) revoke(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
}

func (s *sessions) pruneLocked(now time.Time) {
	for id, expires := range s.byID {
		if !now.Before(expires) {
			delete(s.byID, id)
		}
	}
	for len(s.byID) >= maxSessions {
		var oldest string
		var oldestAt time.Time
		for id, expires := range s.byID {
			if oldest == "" || expires.Before(oldestAt) {
				oldest, oldestAt = id, expires
			}
		}
		delete(s.byID, oldest)
	}
}

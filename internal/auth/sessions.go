package auth

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
)

// session is one browser sign-in. The cookie value is held only as a digest;
// handle is the public name used in actors, audit records and revocation.
type session struct {
	key      string
	handle   string
	actorID  string
	ident    identity.Identity
	csrf     string
	expires  time.Time
	stepUpAt time.Time
}

func (ss *session) actor() core.Actor {
	return core.Actor{ID: ss.actorID, Kind: core.ActorHuman, SessionID: ss.handle, StepUpAt: ss.stepUpAt}
}

// createSession opens a session for id and returns the cookie value.
func (s *Service) createSession(id identity.Identity) (cookie string, ss *session, err error) {
	cookie, err = randomToken(32)
	if err != nil {
		return "", nil, err
	}
	handle, err := randomToken(12)
	if err != nil {
		return "", nil, err
	}
	csrf, err := randomToken(32)
	if err != nil {
		return "", nil, err
	}
	now := s.cfg.Clock.Now()
	ss = &session{key: digest(cookie), handle: "ses_" + handle, actorID: ActorID(id), ident: id, csrf: csrf,
		expires: now.Add(s.cfg.SessionTTL)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneSessionsLocked(now)
	s.sessions[ss.key] = ss
	s.byHandle[ss.handle] = ss
	s.accounts[ss.actorID] = id
	return cookie, ss, nil
}

// liveSessionLocked returns the unexpired session stored under key.
func (s *Service) liveSessionLocked(key string) (*session, bool) {
	ss, ok := s.sessions[key]
	if !ok {
		return nil, false
	}
	if !s.cfg.Clock.Now().Before(ss.expires) {
		s.dropSessionLocked(ss)
		return nil, false
	}
	return ss, true
}

func (s *Service) dropSessionLocked(ss *session) {
	delete(s.sessions, ss.key)
	delete(s.byHandle, ss.handle)
}

// pruneSessionsLocked removes expired sessions and, at the cap, the ones
// closest to expiry.
func (s *Service) pruneSessionsLocked(now time.Time) {
	for _, ss := range s.sessions {
		if !now.Before(ss.expires) {
			s.dropSessionLocked(ss)
		}
	}
	for len(s.sessions) >= s.cfg.MaxSessions {
		var oldest *session
		for _, ss := range s.sessions {
			if oldest == nil || ss.expires.Before(oldest.expires) {
				oldest = ss
			}
		}
		s.dropSessionLocked(oldest)
	}
}

// Active reports whether handle names a live session or token. The
// authorizer calls it at action time, so revocation takes effect for actors
// that were authenticated before it.
func (s *Service) Active(_ context.Context, handle string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss, ok := s.byHandle[handle]; ok {
		_, live := s.liveSessionLocked(ss.key)
		return live
	}
	if t, ok := s.tokenByID[handle]; ok {
		_, live := s.liveTokenLocked(t.key)
		return live
	}
	return false
}

// RevokeSession ends the session with the public handle. It returns
// NOT_FOUND for an unknown or already ended session.
func (s *Service) RevokeSession(handle string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.byHandle[handle]
	if !ok {
		return core.NewError(core.CodeNotFound, "no such session")
	}
	s.dropSessionLocked(ss)
	return nil
}

// RevokeActor ends every session and token of actorID and reports how many
// credentials it ended.
func (s *Service) RevokeActor(actorID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, ss := range s.sessions {
		if ss.actorID == actorID {
			s.dropSessionLocked(ss)
			n++
		}
	}
	for _, t := range s.tokens {
		if t.info.ActorID == actorID {
			s.dropTokenLocked(t)
			n++
		}
	}
	return n
}

// markStepUp records a completed step-up on the session under key, which
// must still be live and belong to the same issuer and subject.
func (s *Service) markStepUp(key string, id identity.Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.liveSessionLocked(key)
	switch {
	case !ok:
		return unauthenticated("the session ended before step-up completed")
	case !ss.ident.Same(id):
		return unauthenticated("step-up must be completed by the person who is signed in")
	}
	ss.stepUpAt = s.cfg.Clock.Now()
	return nil
}

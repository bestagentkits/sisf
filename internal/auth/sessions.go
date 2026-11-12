package auth

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
)

// session is one browser sign-in. The cookie value is held only as a digest;
// handle is the public name used in actors, audit records and revocation.
// proofs are the step-up confirmations not yet spent, each for one call.
type session struct {
	key     string
	handle  string
	actorID string
	ident   identity.Identity
	csrf    string
	expires time.Time
	proofs  map[StepUpTarget]stepUpProof
}

// actor is the session's person. It never carries a step-up time: a
// confirmation is bound to one call and is spent through ConsumeStepUp, so
// nothing that merely rides the session inherits it.
func (ss *session) actor() core.Actor {
	return core.Actor{ID: ss.actorID, Kind: core.ActorHuman, SessionID: ss.handle}
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

// pruneSessionsLocked makes room for a new session. Anyone with an account
// at a provider can sign in, so it removes expired sessions and then, while
// all MaxSessions are taken, one of a person who holds no role
// (Config.HoldsRole), from whoever holds the most: role-less sign-ins never
// sign out a person who holds a role, and one account signing in again and
// again loses its own sessions before anyone else's. Among the candidates
// the session closest to expiry goes first. Last, it forgets the identity of every actor left with
// no session and no token.
func (s *Service) pruneSessionsLocked(now time.Time) {
	perActor := map[string]int{}
	for _, ss := range s.sessions {
		if !now.Before(ss.expires) {
			s.dropSessionLocked(ss)
			continue
		}
		perActor[ss.actorID]++
	}
	holdsRole := func(actor string) bool { return s.cfg.HoldsRole != nil && s.cfg.HoldsRole(actor) }
	for len(s.sessions) >= s.cfg.MaxSessions {
		busiest := 0
		for actor, n := range perActor {
			if !holdsRole(actor) {
				busiest = max(busiest, n)
			}
		}
		s.dropFirstSessionLocked(perActor, func(ss *session) bool {
			return busiest == 0 || !holdsRole(ss.actorID) && perActor[ss.actorID] == busiest
		})
	}
	s.forgetIdleAccountsLocked()
}

// dropFirstSessionLocked drops the session closest to expiry among those
// eligible picks, keeping perActor, the count of sessions by actor, current.
func (s *Service) dropFirstSessionLocked(perActor map[string]int, eligible func(*session) bool) {
	var victim *session
	for _, ss := range s.sessions {
		if eligible(ss) && (victim == nil || ss.expires.Before(victim.expires)) {
			victim = ss
		}
	}
	if victim == nil {
		return
	}
	s.dropSessionLocked(victim)
	perActor[victim.actorID]--
}

// forgetIdleAccountsLocked forgets the identity of each actor that holds no
// session and no token; it is needed only while one of them lives.
func (s *Service) forgetIdleAccountsLocked() {
	live := make(map[string]bool, len(s.sessions)+len(s.tokens))
	for _, ss := range s.sessions {
		live[ss.actorID] = true
	}
	for _, t := range s.tokens {
		live[t.info.ActorID] = true
	}
	for actorID := range s.accounts {
		if !live[actorID] {
			delete(s.accounts, actorID)
		}
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

// markStepUp records a completed step-up for target on the session under
// key, which must still be live and belong to the same issuer and subject.
func (s *Service) markStepUp(key string, id identity.Identity, target StepUpTarget) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.liveSessionLocked(key)
	switch {
	case !ok:
		return unauthenticated("the session ended before step-up completed")
	case !ss.ident.Same(id):
		return unauthenticated("step-up must be completed by the person who is signed in")
	}
	s.addProofLocked(ss, target, s.cfg.Clock.Now())
	return nil
}

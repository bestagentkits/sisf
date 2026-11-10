package auth

import (
	"slices"

	"github.com/bestagentkits/sisf/pkg/core"
)

func (s *Service) liveTokenLocked(key string) (*token, bool) {
	t, ok := s.tokens[key]
	if !ok {
		return nil, false
	}
	if !s.cfg.Clock.Now().Before(t.info.ExpiresAt) {
		s.dropTokenLocked(t)
		return nil, false
	}
	return t, true
}

func (s *Service) dropTokenLocked(t *token) {
	delete(s.tokens, t.key)
	delete(s.tokenByID, t.info.ID)
}

// RevokeToken ends the token with id. It returns NOT_FOUND for an unknown
// or already ended token.
func (s *Service) RevokeToken(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokenByID[id]
	if !ok {
		return core.NewError(core.CodeNotFound, "no such token")
	}
	s.dropTokenLocked(t)
	return nil
}

// Tokens lists the live tokens of actorID, without secrets.
func (s *Service) Tokens(actorID string) []TokenInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []TokenInfo
	for _, t := range s.tokens {
		if live, ok := s.liveTokenLocked(t.key); ok && live.info.ActorID == actorID {
			out = append(out, live.info)
		}
	}
	slices.SortFunc(out, func(a, b TokenInfo) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

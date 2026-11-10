package oauthas

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"regexp"
	"slices"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// codeGrant is what an authorization code stands for. A used grant is kept
// until the token it produced has expired, so a replayed code is detected
// and revokes that token (RFC 6749 section 4.1.2).
type codeGrant struct {
	clientID    string
	redirectURI string
	challenge   string
	resource    string
	scopes      []string
	actorID     string
	session     string
	expires     time.Time
	used        bool
	tokenID     string
	forgetAt    time.Time
}

const maxCodes = 4096

// issueCode stores a grant for req and returns its single-use code.
func (s *Server) issueCode(req authRequest, actor core.Actor) (string, error) {
	code, err := randomToken(32)
	if err != nil {
		return "", err
	}
	now := s.cfg.Clock.Now()
	g := &codeGrant{clientID: req.client.ID, redirectURI: req.redirectURI, challenge: req.challenge,
		resource: req.resource, scopes: slices.Clone(req.scopes), actorID: actor.ID, session: actor.SessionID,
		expires: now.Add(s.cfg.CodeTTL)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	if len(s.codes) >= maxCodes {
		return "", core.NewError(core.CodeUnavailable, "too many codes are outstanding; try again later")
	}
	s.codes[digest(code)] = g
	return code, nil
}

// redeemCode marks the grant behind code used, names the token it will
// produce, and returns a copy. A code that is unknown, expired or used
// before fails; a reuse also revokes the token the first use produced. The
// caller then checks the binding.
func (s *Server) redeemCode(code string) (codeGrant, error) {
	tokenID, err := randomToken(16)
	if err != nil {
		return codeGrant{}, err
	}
	now := s.cfg.Clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.codes[digest(code)]
	switch {
	case !ok:
		return codeGrant{}, core.NewError(core.CodeNotFound, "the authorization code is unknown")
	case g.used:
		s.revoked[g.tokenID] = g.forgetAt
		return codeGrant{}, core.NewError(core.CodeReplayed, "the authorization code was already used; its token is revoked")
	case !now.Before(g.expires):
		delete(s.codes, digest(code))
		return codeGrant{}, core.NewError(core.CodeExpired, "the authorization code has expired")
	}
	// The token is minted at now and lives AccessTokenTTL; the margin covers
	// the moment between this line and the minting.
	g.used, g.tokenID, g.forgetAt = true, tokenID, now.Add(s.cfg.AccessTokenTTL+time.Minute)
	out := *g
	out.scopes = slices.Clone(g.scopes)
	return out, nil
}

// isRevoked reports whether the token with id was revoked.
func (s *Server) isRevoked(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.revoked[id]
	return ok
}

// pruneLocked forgets expired codes, consents and revocations.
func (s *Server) pruneLocked(now time.Time) {
	for k, g := range s.codes {
		if (!g.used && !now.Before(g.expires)) || (g.used && !now.Before(g.forgetAt)) {
			delete(s.codes, k)
		}
	}
	for id, until := range s.revoked {
		if !now.Before(until) {
			delete(s.revoked, id)
		}
	}
}

// verifierPattern is a PKCE code verifier (RFC 7636 section 4.1).
var verifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

// pkceMatches reports whether verifier hashes to challenge under S256.
func pkceMatches(challenge, verifier string) bool {
	if !verifierPattern.MatchString(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

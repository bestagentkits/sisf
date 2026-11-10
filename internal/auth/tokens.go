package auth

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// TokenKind tells a personal access token from a service token.
type TokenKind string

const (
	// TokenPAT is a person's token for the API and CLI.
	TokenPAT TokenKind = "pat"
	// TokenService is an agent's or a service's token.
	TokenService TokenKind = "service"
)

// Token prefixes make leaked tokens recognizable to secret scanners.
const (
	patPrefix     = "sisf_pat_"
	servicePrefix = "sisf_svc_"
	maxScopes     = 32
)

// scopePattern admits names such as "runs:read" or "approvals.decide"; a
// wildcard is never a scope.
var scopePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*([.:][a-z][a-z0-9_]*)*$`)

// TokenInfo describes a token without its secret.
type TokenInfo struct {
	ID        string         `json:"id"`
	Name      string         `json:"name,omitempty"`
	Kind      TokenKind      `json:"kind"`
	ActorID   string         `json:"actor_id"`
	ActorKind core.ActorKind `json:"actor_kind"`
	Scopes    []string       `json:"scopes"`
	CreatedAt time.Time      `json:"created_at"`
	ExpiresAt time.Time      `json:"expires_at"`
}

// IssuedToken is a new token. Token is shown to its holder once and is
// redacted whenever it is printed or encoded.
type IssuedToken struct {
	TokenInfo
	Token core.SecretValue `json:"-"`
}

// PATRequest asks for a personal access token. TTL zero means DefaultPATTTL.
type PATRequest struct {
	Name   string        `json:"name"`
	Scopes []string      `json:"scopes"`
	TTL    time.Duration `json:"-"`
}

// ServiceTokenRequest asks for a service token for an agent or service.
// ActorID must be "agent:<name>" for an agent or "service:<name>" for a
// service, so a service token can never stand for a person.
type ServiceTokenRequest struct {
	ActorID string
	Kind    core.ActorKind
	Name    string
	Scopes  []string
	// TTL zero means DefaultServiceTokenTTL.
	TTL time.Duration
}

type token struct {
	key  string
	info TokenInfo
}

// IssuePAT mints a personal access token for the person signed in to the
// session named by actor.SessionID. Tokens cannot mint tokens, and the
// session must hold an unspent step-up made to mint a token: a token
// outlives the session it was minted from, so minting one is a sensitive
// act, and each confirmation mints one token at most.
func (s *Service) IssuePAT(actor core.Actor, req PATRequest) (IssuedToken, error) {
	s.mu.Lock()
	ss, ok := s.byHandle[actor.SessionID]
	if ok {
		ss, ok = s.liveSessionLocked(ss.key)
	}
	if !ok || ss.actorID != actor.ID || actor.Kind != core.ActorHuman {
		s.mu.Unlock()
		return IssuedToken{}, unauthenticated("a personal access token is minted only from a live browser session")
	}
	// A request that cannot be honoured is refused before the confirmation
	// is spent.
	if err := s.checkScopes(req.Scopes); err != nil {
		s.mu.Unlock()
		return IssuedToken{}, err
	}
	if req.TTL < 0 || req.TTL > MaxPATTTL {
		s.mu.Unlock()
		return IssuedToken{}, invalidArgument("a token may live at most " + MaxPATTTL.String())
	}
	_, err := s.consumeProofLocked(ss, StepUpTarget{Action: TokenStepUpAction}, s.cfg.Clock.Now())
	s.mu.Unlock()
	if err != nil {
		return IssuedToken{}, core.NewError(core.CodeStepUpRequired, "confirm your identity again before minting a token")
	}
	return s.issue(TokenPAT, actor.ID, core.ActorHuman, req.Name, req.Scopes, req.TTL, DefaultPATTTL, MaxPATTTL)
}

// IssueServiceToken mints a token for an agent or a service. It is called
// by trusted factory code, never on behalf of a request.
func (s *Service) IssueServiceToken(req ServiceTokenRequest) (IssuedToken, error) {
	name, ok := strings.CutPrefix(req.ActorID, string(req.Kind)+":")
	if req.Kind != core.ActorAgent && req.Kind != core.ActorService || !ok || !scopePattern.MatchString(name) {
		return IssuedToken{}, invalidArgument(`a service token is for an agent ("agent:<name>") or a service ("service:<name>"), never a person`)
	}
	return s.issue(TokenService, req.ActorID, req.Kind, req.Name, req.Scopes, req.TTL, DefaultServiceTokenTTL, MaxServiceTokenTTL)
}

func (s *Service) issue(kind TokenKind, actorID string, actorKind core.ActorKind, name string, scopes []string,
	ttl, defTTL, maxTTL time.Duration) (IssuedToken, error) {
	if err := s.checkScopes(scopes); err != nil {
		return IssuedToken{}, err
	}
	switch {
	case ttl < 0 || ttl > maxTTL:
		return IssuedToken{}, invalidArgument("a token may live at most " + maxTTL.String())
	case ttl == 0:
		ttl = defTTL
	}
	secret, err := randomToken(32)
	if err != nil {
		return IssuedToken{}, err
	}
	id, err := randomToken(12)
	if err != nil {
		return IssuedToken{}, err
	}
	prefix := patPrefix
	if kind == TokenService {
		prefix = servicePrefix
	}
	raw := prefix + secret
	now := s.cfg.Clock.Now()
	t := &token{key: digest(raw), info: TokenInfo{ID: "tok_" + id, Name: name, Kind: kind, ActorID: actorID,
		ActorKind: actorKind, Scopes: slices.Clone(scopes), CreatedAt: now, ExpiresAt: now.Add(ttl)}}
	s.mu.Lock()
	s.tokens[t.key] = t
	s.tokenByID[t.info.ID] = t
	s.mu.Unlock()
	return IssuedToken{TokenInfo: t.info, Token: core.NewSecretValue([]byte(raw))}, nil
}

// checkScopes requires one or more well-formed, distinct, allowed scopes.
func (s *Service) checkScopes(scopes []string) error {
	if len(scopes) == 0 || len(scopes) > maxScopes {
		return invalidArgument("a token needs between 1 and 32 scopes")
	}
	seen := map[string]bool{}
	for _, sc := range scopes {
		switch {
		case !scopePattern.MatchString(sc):
			return invalidArgument("scope " + sc + " is malformed")
		case seen[sc]:
			return invalidArgument("scope " + sc + " is repeated")
		case len(s.cfg.AllowedScopes) > 0 && !slices.Contains(s.cfg.AllowedScopes, sc):
			return invalidArgument("scope " + sc + " is not a known scope")
		}
		seen[sc] = true
	}
	return nil
}

// authenticateToken maps a presented bearer token to its actor. A token
// never carries a step-up, whatever session minted it.
func (s *Service) authenticateToken(raw string) (core.Actor, error) {
	if !strings.HasPrefix(raw, patPrefix) && !strings.HasPrefix(raw, servicePrefix) {
		return core.Actor{}, unauthenticated("the bearer token is not a factory token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.liveTokenLocked(digest(raw))
	if !ok {
		return core.Actor{}, unauthenticated("the bearer token is unknown, expired or revoked")
	}
	return core.Actor{ID: t.info.ActorID, Kind: t.info.ActorKind, Scopes: slices.Clone(t.info.Scopes), SessionID: t.info.ID}, nil
}

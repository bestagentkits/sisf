package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
)

// Defaults for Config fields left at zero. They are starting values.
const (
	DefaultSessionTTL      = 12 * time.Hour
	DefaultPATTTL          = 8 * time.Hour
	MaxPATTTL              = 7 * 24 * time.Hour
	DefaultServiceTokenTTL = 15 * time.Minute
	MaxServiceTokenTTL     = time.Hour
	DefaultMaxSessions     = 10000
	flowTTL                = 10 * time.Minute
	maxFlows               = 1024
	// maxStepUpsPerSession bounds the step-ups one session has open.
	maxStepUpsPerSession = 4
	userActorPrefix      = "user:"
)

// Config configures the authentication service.
type Config struct {
	// Providers are the login providers, each with a unique name.
	Providers []identity.Provider
	Clock     core.Clock
	// SessionTTL is how long a browser session lasts.
	SessionTTL time.Duration
	// StepUpMaxAge is how recent a step-up must be to mint a token. It
	// defaults to the approval service's value so both agree.
	StepUpMaxAge time.Duration
	// AllowedScopes, when set, is the only scopes a token may carry.
	AllowedScopes []string
	// SecureCookies adds the Secure attribute to every cookie. A request
	// that arrived over TLS gets it regardless.
	SecureCookies bool
	// MaxSessions bounds memory. At the bound a session of a person who
	// holds no role goes first, from whoever holds the most sessions, the
	// one closest to expiry first.
	MaxSessions int
	// HoldsRole reports whether an actor holds a role by a local grant, such
	// as the Owner. It is read only to choose which session or step-up to
	// drop when room is needed, never to authorize, and must not block: it
	// is called with the service's lock held. Nil means nobody does.
	HoldsRole func(actorID string) bool
	// ClientKey names the network source of a request: when every sign-in
	// slot is taken, the flows of the source holding the most go first, and
	// when no source holds more than one, a flow chosen at random makes room
	// for the new sign-in.
	// control.ClientAddress builds one that names an IPv6 client by its /64.
	// Nil means the remote IP address.
	ClientKey func(*http.Request) string
}

// Service authenticates callers and owns sessions, tokens and sign-in flows.
// State is in memory: a restart signs everyone out and voids every token,
// which is the safe failure.
type Service struct {
	cfg       Config
	providers map[string]identity.Provider

	mu        sync.Mutex
	sessions  map[string]*session // by digest of the cookie value
	byHandle  map[string]*session // by public handle
	tokens    map[string]*token   // by digest of the token
	tokenByID map[string]*token
	flows     map[string]*flow // by digest of the state
	flowSeq   uint64
	// pick chooses which of n flows to push out when every source holds
	// one; nil means randomIndex.
	pick     func(n int) (int, error)
	accounts map[string]identity.Identity
}

// New validates cfg and returns a service.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Clock == nil:
		return nil, invalidArgument("the authentication service needs a clock")
	case len(cfg.Providers) == 0:
		return nil, invalidArgument("the authentication service needs at least one login provider")
	case cfg.SessionTTL < 0 || cfg.StepUpMaxAge < 0 || cfg.MaxSessions < 0:
		return nil, invalidArgument("durations and limits must not be negative")
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = DefaultSessionTTL
	}
	if cfg.StepUpMaxAge == 0 {
		cfg.StepUpMaxAge = approvals.DefaultStepUpMaxAge
	}
	if cfg.MaxSessions == 0 {
		cfg.MaxSessions = DefaultMaxSessions
	}
	s := &Service{cfg: cfg, providers: map[string]identity.Provider{},
		sessions: map[string]*session{}, byHandle: map[string]*session{}, tokens: map[string]*token{},
		tokenByID: map[string]*token{}, flows: map[string]*flow{}, accounts: map[string]identity.Identity{}}
	// An identity is keyed by issuer and subject, so two providers sharing an
	// issuer could each sign in as the other's people.
	issuers := map[string]string{}
	for _, p := range cfg.Providers {
		if p == nil || p.Name() == "" {
			return nil, invalidArgument("a login provider needs a name")
		}
		if _, dup := s.providers[p.Name()]; dup {
			return nil, invalidArgument("two login providers are named " + p.Name())
		}
		if other, dup := issuers[p.Issuer()]; dup {
			return nil, invalidArgument("login providers " + other + " and " + p.Name() + " share the issuer " + p.Issuer() + "; give each its own")
		}
		s.providers[p.Name()], issuers[p.Issuer()] = p, p.Name()
	}
	for _, scope := range cfg.AllowedScopes {
		if !scopePattern.MatchString(scope) {
			return nil, invalidArgument("allowed scope " + scope + " is malformed")
		}
	}
	return s, nil
}

// ActorID is the internal actor ID of a login identity. It is derived from
// issuer and subject only, so the same person at another provider, or anyone
// sharing an email address, is a different actor. Local grants, such as the
// Owner's, are recorded against this ID.
func ActorID(id identity.Identity) string {
	sum := sha256.Sum256([]byte(id.Key()))
	return userActorPrefix + hex.EncodeToString(sum[:16])
}

// GitHubLogin returns the GitHub login of a human actor that signed in with
// a GitHub provider, for membership checks. ok is false for anyone else.
func (s *Service) GitHubLogin(_ context.Context, actorID string) (login string, ok bool, err error) {
	s.mu.Lock()
	id, known := s.accounts[actorID]
	s.mu.Unlock()
	if !known || id.Login == "" {
		return "", false, nil
	}
	for _, p := range s.providers {
		if gh, isGitHub := p.(*GitHubProvider); isGitHub && gh.Issuer() == id.Issuer {
			return id.Login, true, nil
		}
	}
	return "", false, nil
}

// Identity returns the login identity last seen for actorID.
func (s *Service) Identity(actorID string) (identity.Identity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.accounts[actorID]
	return id, ok
}

// freshStepUp reports whether at is a step-up recent enough at now.
func (s *Service) freshStepUp(at, now time.Time) bool {
	return !at.IsZero() && !at.After(now) && now.Sub(at) <= s.cfg.StepUpMaxAge
}

// clientKey names the network source of r: Config.ClientKey, or else the
// remote IP address.
func (s *Service) clientKey(r *http.Request) string {
	if s.cfg.ClientKey != nil {
		return s.cfg.ClientKey(r)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

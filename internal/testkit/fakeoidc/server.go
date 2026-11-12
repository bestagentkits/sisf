// Package fakeoidc is an in-memory identity provider for tests and the
// simulation profile. It speaks two dialects on one listener:
//
//   - Google-style OpenID Connect: discovery, a JWKS, an authorization
//     endpoint that honors nonce, PKCE, prompt=login and max_age, and a token
//     endpoint that returns signed ID tokens.
//   - GitHub-style OAuth: an authorization endpoint, an access-token
//     endpoint, the /user API and the repository collaborator permission API.
//
// It has no login page. The authorization endpoints sign in as the user
// chosen with SignInAs, so a test drives a whole browser flow with plain HTTP
// requests. Faults make the next tokens wrong in one chosen way so a relying
// party's validation can be proven.
package fakeoidc

import (
	"crypto/rsa"
	"net/http"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// User is a person the fake can sign in as.
type User struct {
	// Subject is the OIDC "sub" claim.
	Subject       string `json:"subject"`
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified,omitempty"`
	Name          string `json:"name,omitempty"`
	// Login and GitHubID are what the GitHub-style API reports.
	Login    string `json:"login,omitempty"`
	GitHubID int64  `json:"github_id,omitempty"`
}

// Client is a registered relying party. An empty Secret makes the client
// public: the token endpoint then relies on PKCE alone.
type Client struct {
	ID           string   `json:"client_id"`
	Secret       string   `json:"client_secret,omitempty"`
	RedirectURIs []string `json:"redirect_uris"`
}

// Faults make issued tokens wrong in one way each. The zero value issues
// correct tokens. A fault stays active until it is changed.
type Faults struct {
	// Issuer replaces the "iss" claim.
	Issuer string `json:"issuer,omitempty"`
	// Audience replaces the "aud" claim.
	Audience string `json:"audience,omitempty"`
	// Nonce replaces the "nonce" claim.
	Nonce string `json:"nonce,omitempty"`
	// Lifetime replaces the default ID token lifetime; a negative value
	// issues a token that is already expired.
	Lifetime time.Duration `json:"lifetime,omitempty"`
	// UnknownKey signs with a key the JWKS does not publish.
	UnknownKey bool `json:"unknown_key,omitempty"`
	// IgnoreReauth makes the provider skip the fresh sign-in asked for by
	// prompt=login or max_age=0, so auth_time stays old.
	IgnoreReauth bool `json:"ignore_reauth,omitempty"`
}

// DefaultTokenLifetime is how long an ID token or access token lives.
const DefaultTokenLifetime = time.Hour

// codeLifetime is how long an authorization code may wait to be exchanged.
const codeLifetime = time.Minute

// Server is the fake provider. It is safe for concurrent use.
type Server struct {
	issuer  string
	clock   core.Clock
	signing *rsa.PrivateKey
	rogue   *rsa.PrivateKey
	keyID   string
	handler http.Handler

	mu          sync.Mutex
	users       map[string]User
	authAt      map[string]time.Time
	current     string
	clients     map[string]Client
	codes       map[string]grant
	accessToken map[string]accessGrant
	perms       map[string]string
	faults      Faults
}

// grant is an issued authorization code waiting to be exchanged.
type grant struct {
	clientID    string
	redirectURI string
	subject     string
	nonce       string
	challenge   string
	github      bool
	expires     time.Time
}

// accessGrant is an issued access token.
type accessGrant struct {
	subject string
	expires time.Time
}

// Option configures a Server.
type Option func(*Server)

// WithClock makes the server read time from clock instead of the wall clock.
func WithClock(clock core.Clock) Option { return func(s *Server) { s.clock = clock } }

// New returns a fake provider whose issuer is issuer, the base URL it is
// served at. It starts with no users and no clients.
func New(issuer string, opts ...Option) (*Server, error) {
	signing, rogue, err := newKeys()
	if err != nil {
		return nil, err
	}
	s := &Server{
		issuer: issuer, clock: core.SystemClock{}, signing: signing, rogue: rogue, keyID: keyID(&signing.PublicKey),
		users: map[string]User{}, authAt: map[string]time.Time{}, clients: map[string]Client{},
		codes: map[string]grant{}, accessToken: map[string]accessGrant{}, perms: map[string]string{},
	}
	for _, opt := range opts {
		opt(s)
	}
	s.handler = s.routes()
	return s, nil
}

// Issuer returns the issuer URL every ID token carries.
func (s *Server) Issuer() string { return s.issuer }

// ServeHTTP serves both dialects and the control endpoints.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// AddUser adds or replaces a user. Adding a user does not sign them in.
func (s *Server) AddUser(u User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[u.Subject] = u
}

// SignInAs makes subject the person the authorization endpoints sign in, as
// if they had just entered their credentials. It reports whether the user
// exists.
func (s *Server) SignInAs(subject string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[subject]; !ok {
		return false
	}
	s.current = subject
	s.authAt[subject] = s.clock.Now()
	return true
}

// AddClient registers a relying party.
func (s *Server) AddClient(c Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[c.ID] = c
}

// SetFaults replaces the active faults.
func (s *Server) SetFaults(f Faults) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = f
}

// SetRepoPermission records the GitHub permission login has on repo
// ("owner/name"): admin, maintain, write, triage, read, or "" for none.
func (s *Server) SetRepoPermission(repo, login, permission string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if permission == "" {
		delete(s.perms, repo+"|"+login)
		return
	}
	s.perms[repo+"|"+login] = permission
}

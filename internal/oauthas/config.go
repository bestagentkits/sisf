package oauthas

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Paths served by Handler. Mount Handler for the two well-known prefixes and
// for OAuthPrefix.
const (
	AuthorizationServerMetadataPath = "/.well-known/oauth-authorization-server"
	ProtectedResourceMetadataPrefix = "/.well-known/oauth-protected-resource"
	OAuthPrefix                     = "/oauth/"
	AuthorizePath                   = "/oauth/authorize"
	TokenPath                       = "/oauth/token"
	RegisterPath                    = "/oauth/register"
)

// Defaults and bounds for Config fields left at zero.
const (
	DefaultAccessTokenTTL = 10 * time.Minute
	MaxAccessTokenTTL     = time.Hour
	DefaultCodeTTL        = time.Minute
	MaxCodeTTL            = 5 * time.Minute
	DefaultMaxClients     = 1000
	consentTTL            = 10 * time.Minute
	maxPending            = 4096
)

// Sessions is what the server needs from the authentication service;
// *auth.Service is one. Authenticate reads the person's browser session and
// Active reports whether a session handle is still live.
type Sessions interface {
	Authenticate(r *http.Request) (core.Actor, error)
	Active(ctx context.Context, handle string) bool
}

// Config configures a Server.
type Config struct {
	// Issuer is this server's issuer identifier: an https origin, or an http
	// loopback origin for local use, with no path.
	Issuer string
	// Resource is the canonical URI of the protected MCP endpoint. Every
	// token is bound to it, and a request for any other resource is refused.
	Resource string
	// ResourceName is shown in the resource metadata and on the consent page.
	ResourceName string
	// LoginPath is the local page where a person signs in. The server sends
	// a person without a session there with ?return_to=<authorize request>.
	LoginPath string
	Sessions  Sessions
	// Scopes decides which scopes a person may delegate right now.
	Scopes ScopePolicy
	// ScopesSupported is published in both metadata documents.
	ScopesSupported []string
	// Clients are public clients known in advance.
	Clients []Client
	// DynamicRegistration opens the registration endpoint (RFC 7591).
	DynamicRegistration bool
	// MaxClients bounds dynamically registered clients.
	MaxClients int
	// ClientKey names the network source of a registration: when MaxClients
	// are registered, the unused clients of the source holding the most go
	// first, and a new one is refused when no source holds more than one.
	// control.ClientAddress builds one that names an IPv6 client by its /64.
	// Nil means the remote IP address.
	ClientKey func(*http.Request) string
	// SigningKey signs access tokens; nil generates one, so a restart voids
	// every token, which is the safe failure.
	SigningKey *ecdsa.PrivateKey
	Clock      core.Clock
	// AccessTokenTTL and CodeTTL default to DefaultAccessTokenTTL and
	// DefaultCodeTTL and are capped by the Max values.
	AccessTokenTTL time.Duration
	CodeTTL        time.Duration
}

// Server is the authorization server. State is in memory.
type Server struct {
	cfg Config

	mu      sync.Mutex
	clients map[string]Client
	// dynamic holds the bookkeeping of the dynamically registered clients.
	dynamic   map[string]*clientUse
	clientSeq uint64
	pending   map[string]*pendingConsent // by digest of the consent ID
	codes     map[string]*codeGrant      // by digest of the code
	revoked   map[string]time.Time       // token ID -> its expiry
}

// New validates cfg and returns a server.
func New(cfg Config) (*Server, error) {
	if err := validateOrigin("issuer", cfg.Issuer, false); err != nil {
		return nil, err
	}
	if err := validateOrigin("resource", cfg.Resource, true); err != nil {
		return nil, err
	}
	switch {
	case cfg.Sessions == nil || cfg.Scopes == nil || cfg.Clock == nil:
		return nil, invalidConfig("the authorization server needs sessions, a scope policy and a clock")
	case !strings.HasPrefix(cfg.LoginPath, "/") || strings.HasPrefix(cfg.LoginPath, "//"):
		return nil, invalidConfig("the login path must be a local path")
	case cfg.AccessTokenTTL < 0 || cfg.AccessTokenTTL > MaxAccessTokenTTL:
		return nil, invalidConfig("an access token may live at most " + MaxAccessTokenTTL.String())
	case cfg.CodeTTL < 0 || cfg.CodeTTL > MaxCodeTTL:
		return nil, invalidConfig("an authorization code may live at most " + MaxCodeTTL.String())
	case cfg.MaxClients < 0:
		return nil, invalidConfig("the client limit must not be negative")
	}
	for _, sc := range cfg.ScopesSupported {
		if !scopePattern.MatchString(sc) {
			return nil, invalidConfig("supported scope " + sc + " is malformed")
		}
	}
	if cfg.AccessTokenTTL == 0 {
		cfg.AccessTokenTTL = DefaultAccessTokenTTL
	}
	if cfg.CodeTTL == 0 {
		cfg.CodeTTL = DefaultCodeTTL
	}
	if cfg.MaxClients == 0 {
		cfg.MaxClients = DefaultMaxClients
	}
	if cfg.SigningKey == nil {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, core.NewError(core.CodeInternal, "no signing key could be generated").WithCause(err)
		}
		cfg.SigningKey = key
	} else if cfg.SigningKey.Curve != elliptic.P256() {
		return nil, invalidConfig("the signing key must be a P-256 key")
	}
	s := &Server{cfg: cfg, clients: map[string]Client{}, dynamic: map[string]*clientUse{}, pending: map[string]*pendingConsent{},
		codes: map[string]*codeGrant{}, revoked: map[string]time.Time{}}
	for _, c := range cfg.Clients {
		if err := c.validate(); err != nil {
			return nil, err
		}
		if _, dup := s.clients[c.ID]; dup {
			return nil, invalidConfig("two clients are named " + c.ID)
		}
		s.clients[c.ID] = c.clone()
	}
	return s, nil
}

// Issuer returns the issuer identifier.
func (s *Server) Issuer() string { return s.cfg.Issuer }

// Resource returns the canonical URI of the protected resource.
func (s *Server) Resource() string { return s.cfg.Resource }

// resourcePathPattern is a path of plain segments, so it is safe to use in
// a route pattern.
var resourcePathPattern = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)*$`)

// validateOrigin accepts an absolute https URL, or http on a loopback host,
// without query, fragment or user info. A path is allowed only when
// withPath is set.
func validateOrigin(label, raw string, withPath bool) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(raw, "#"):
		return invalidConfig("the " + label + " must be an absolute URL without query or fragment")
	case u.Scheme != "https" && (u.Scheme != "http" || !loopbackHost(u.Hostname())):
		return invalidConfig("the " + label + " must use https; http is allowed only on a loopback host")
	case !withPath && u.Path != "":
		return invalidConfig("the " + label + " must be an origin with no path")
	case !resourcePathPattern.MatchString(u.Path) || u.RawPath != "":
		return invalidConfig("the " + label + " path must be plain segments with no trailing slash")
	}
	return nil
}

// loopbackHost reports whether host is localhost or a loopback address.
func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func invalidConfig(message string) *core.Error {
	return core.NewError(core.CodeInvalidArgument, message)
}

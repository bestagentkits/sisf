package oauthas

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bestagentkits/sisf/internal/intake/report"
)

// Client is a public OAuth client. No client has a secret: PKCE binds each
// code to the party that asked for it.
type Client struct {
	ID   string `json:"client_id"`
	Name string `json:"client_name,omitempty"`
	// RedirectURIs are matched exactly, character for character.
	RedirectURIs []string `json:"redirect_uris"`
	// Dynamic marks a client added by dynamic registration; the consent page
	// says its name is unverified.
	Dynamic bool `json:"-"`
}

const (
	maxRedirectURIs = 5
	maxClientName   = 100
	maxBodyBytes    = 16 << 10
)

var clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

func (c Client) validate() error {
	switch {
	case !clientIDPattern.MatchString(c.ID):
		return invalidConfig("client ID " + c.ID + " is malformed")
	case len(c.RedirectURIs) == 0 || len(c.RedirectURIs) > maxRedirectURIs:
		return invalidConfig("a client needs between 1 and 5 redirect URIs")
	case utf8.RuneCountInString(c.Name) > maxClientName || strings.ContainsFunc(c.Name, isControl):
		return invalidConfig("a client name is at most 100 printable characters")
	}
	for _, uri := range c.RedirectURIs {
		if !validRedirectURI(uri) {
			return invalidConfig("redirect URI " + uri + " must be https, or http on a loopback host, with no fragment")
		}
	}
	return nil
}

func (c Client) clone() Client {
	c.RedirectURIs = slices.Clone(c.RedirectURIs)
	return c
}

// validRedirectURI accepts an absolute https URI, or http on a loopback
// host for native clients (RFC 8252), with no fragment or user info.
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") {
		return false
	}
	return u.Scheme == "https" || u.Scheme == "http" && loopbackHost(u.Hostname())
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// client returns the registered client with id. A dynamically registered
// client that expired is forgotten here.
func (s *Server) client(id string) (Client, bool) {
	now := s.cfg.Clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[id]
	if u, dynamic := s.dynamic[id]; ok && dynamic && u.expired(now) && !s.clientsInFlightLocked(now)[id] {
		s.dropClientLocked(id)
		return Client{}, false
	}
	return c.clone(), ok
}

// registration is the part of an RFC 7591 request the server reads; other
// metadata is ignored.
type registration struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

// handleRegister adds a public client. It asks for nothing and grants
// nothing beyond what any public client gets: a code still needs a signed-in
// person's consent and the PKCE verifier.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if ok, wait := s.admitRegistration(s.clientKey(r)); !ok {
		w.Header().Set("Retry-After", report.RetryAfter(wait))
		writeOAuthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "too many clients are being registered; try again shortly")
		return
	}
	var in registration
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&in); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "the body must be a JSON client registration")
		return
	}
	if m := in.TokenEndpointAuthMethod; m != "" && m != "none" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "only public clients (token_endpoint_auth_method none) are accepted")
		return
	}
	for _, g := range in.GrantTypes {
		// A refresh token is never issued; asking for one is not an error,
		// the answer simply lists what was registered.
		if g != "authorization_code" && g != "refresh_token" {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "only the authorization_code grant is supported")
			return
		}
	}
	for _, rt := range in.ResponseTypes {
		if rt != "code" {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "only the code response type is supported")
			return
		}
	}
	id, err := randomToken(18)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "the client could not be registered")
		return
	}
	c := Client{ID: "mcp_" + id, Name: strings.TrimSpace(in.ClientName), RedirectURIs: in.RedirectURIs, Dynamic: true}
	if err := c.validate(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", messageOf(err))
		return
	}
	if added, wait := s.addDynamicClient(c, s.clientKey(r)); !added {
		w.Header().Set("Retry-After", report.RetryAfter(wait))
		writeOAuthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "too many clients are registered or being registered; try again shortly")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id": c.ID, "client_id_issued_at": s.cfg.Clock.Now().Unix(), "client_name": c.Name,
		"redirect_uris": c.RedirectURIs, "grant_types": []string{"authorization_code"},
		"response_types": []string{"code"}, "token_endpoint_auth_method": "none",
	})
}

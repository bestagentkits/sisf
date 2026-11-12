package oauthas

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// challengePattern is the shape of an S256 code challenge: the base64url
// encoding of a SHA-256 digest, without padding.
var challengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

const maxStateLen = 512

// authRequest is a validated authorization request.
type authRequest struct {
	client      Client
	redirectURI string
	state       string
	challenge   string
	resource    string
	scopes      []string
}

// singleParams may each appear once at most (RFC 6749 section 3.1).
var singleParams = []string{"response_type", "client_id", "redirect_uri", "state", "scope",
	"code_challenge", "code_challenge_method", "resource"}

// handleAuthorize validates the request, makes sure a person is signed in,
// and shows the consent page. Until the client and its redirect URI are
// verified, an error is shown here; after that it goes to the client.
func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch {
	case r.Header.Get("Authorization") != "":
		pageError(w, http.StatusBadRequest, "a client is authorized by a person in a browser session, never with a token")
		return
	case q.Has("client_secret"):
		pageError(w, http.StatusBadRequest, "a client secret must never be sent in a URL; clients here are public and use PKCE")
		return
	}
	for _, p := range singleParams {
		if len(q[p]) > 1 {
			pageError(w, http.StatusBadRequest, "parameter "+p+" appears more than once")
			return
		}
	}
	client, ok := s.client(q.Get("client_id"))
	if !ok {
		pageError(w, http.StatusBadRequest, "the client is not registered")
		return
	}
	redirectURI := q.Get("redirect_uri")
	if !slices.Contains(client.RedirectURIs, redirectURI) {
		pageError(w, http.StatusBadRequest, "the redirect URI does not exactly match one registered for the client")
		return
	}
	state := q.Get("state")
	fail := func(code, description string) { s.redirectError(w, r, redirectURI, state, code, description) }
	scopes, scopesOK := parseScopeParam(q.Get("scope"))
	switch {
	case len(state) > maxStateLen:
		pageError(w, http.StatusBadRequest, "the state parameter is too long")
	case q.Get("response_type") != "code":
		fail("unsupported_response_type", "only the authorization code flow is supported")
	case q.Get("code_challenge_method") != "S256" || !challengePattern.MatchString(q.Get("code_challenge")):
		fail("invalid_request", "PKCE with code_challenge_method S256 is required; plain is refused")
	case q.Get("resource") != s.cfg.Resource:
		fail("invalid_target", "the resource parameter must name this server's protected resource")
	case !scopesOK:
		fail("invalid_scope", "the scope parameter is malformed")
	default:
		s.authorizeSignedIn(w, r, authRequest{client: client, redirectURI: redirectURI, state: state,
			challenge: q.Get("code_challenge"), resource: s.cfg.Resource, scopes: scopes})
	}
}

// authorizeSignedIn continues a valid request: it sends a person without a
// session to sign in, limits the scopes to what they hold, and asks for
// consent.
func (s *Server) authorizeSignedIn(w http.ResponseWriter, r *http.Request, req authRequest) {
	actor, err := s.sessionActor(r)
	switch {
	case core.HasCode(err, core.CodeUnauthenticated):
		http.Redirect(w, r, s.loginURL(r.URL.RequestURI()), http.StatusFound)
		return
	case err != nil:
		pageError(w, http.StatusForbidden, messageOf(err))
		return
	}
	grantable, err := s.cfg.Scopes.GrantableScopes(r.Context(), actor)
	if err != nil {
		s.redirectError(w, r, req.redirectURI, req.state, "server_error", "your scopes could not be determined")
		return
	}
	granted, ok := grantScopes(req.scopes, grantable)
	if !ok {
		s.redirectError(w, r, req.redirectURI, req.state, "invalid_scope", "the requested scopes exceed what you hold")
		return
	}
	req.scopes = granted
	id, err := s.addPending(req, actor)
	if err != nil {
		pageError(w, http.StatusServiceUnavailable, messageOf(err))
		return
	}
	s.renderConsent(w, req, id)
}

// sessionActor returns the person signed in to the browser session of r.
// The consent page carries its own single-use, session-bound consent ID, so
// the session is read as a safe request would be; a token is never accepted.
func (s *Server) sessionActor(r *http.Request) (core.Actor, error) {
	if r.Header.Get("Authorization") != "" {
		return core.Actor{}, core.NewError(core.CodePermissionDenied, "a token cannot authorize a client")
	}
	probe := r.Clone(r.Context())
	probe.Method = http.MethodGet
	probe.Body = http.NoBody
	actor, err := s.cfg.Sessions.Authenticate(probe)
	switch {
	case err != nil:
		return core.Actor{}, err
	case actor.Kind != core.ActorHuman || actor.ID == "" || actor.SessionID == "" || len(actor.Scopes) > 0:
		return core.Actor{}, core.NewError(core.CodePermissionDenied, "only a person in a browser session can authorize a client")
	}
	return actor, nil
}

// loginURL is the sign-in page with a return address back to requestURI.
func (s *Server) loginURL(requestURI string) string {
	sep := "?"
	if strings.Contains(s.cfg.LoginPath, "?") {
		sep = "&"
	}
	return s.cfg.LoginPath + sep + "return_to=" + url.QueryEscape(requestURI)
}

// pendingConsent is an authorization request waiting for the person's
// answer. It is bound to their session and used once.
type pendingConsent struct {
	req     authRequest
	actorID string
	session string
	expires time.Time
}

func (s *Server) addPending(req authRequest, actor core.Actor) (string, error) {
	id, err := randomToken(32)
	if err != nil {
		return "", err
	}
	now := s.cfg.Clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, p := range s.pending {
		if !now.Before(p.expires) {
			delete(s.pending, k)
		}
	}
	if len(s.pending) >= maxPending {
		return "", core.NewError(core.CodeUnavailable, "too many authorizations are waiting; try again later")
	}
	s.pending[digest(id)] = &pendingConsent{req: req, actorID: actor.ID, session: actor.SessionID, expires: now.Add(consentTTL)}
	return id, nil
}

// takePending removes and returns the live pending consent named by id.
func (s *Server) takePending(id string) (*pendingConsent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := digest(id)
	p, ok := s.pending[key]
	delete(s.pending, key)
	if !ok || !s.cfg.Clock.Now().Before(p.expires) {
		return nil, false
	}
	return p, true
}

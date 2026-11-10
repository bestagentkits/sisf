package auth

import (
	"context"
	"crypto/subtle"
	"mime"
	"net/http"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Cookie, header and form names a browser client uses.
const (
	// SessionCookie holds the random session ID. It is HttpOnly and
	// SameSite=Lax: Lax lets the provider's redirect back complete a sign-in,
	// and the CSRF token guards every state-changing request.
	SessionCookie = "sisf_session"
	// FlowCookie binds a sign-in in progress to the browser that started it.
	FlowCookie = "sisf_auth_flow"
	// CSRFHeader carries the session's CSRF token on unsafe requests.
	CSRFHeader = "X-CSRF-Token"
	// CSRFFormField carries it in an HTML form POST instead.
	CSRFFormField = "csrf_token"
)

// Authenticate returns the actor behind r. A request with an Authorization
// header is judged by its bearer token alone, and any cookie is ignored, so a
// token holder never acts with an ambient browser session. Otherwise the
// session cookie is used, and an unsafe method must also carry the session's
// CSRF token. The returned actor has no roles: the authorizer resolves them
// at action time. The caller sets Surface.
func (s *Service) Authenticate(r *http.Request) (core.Actor, error) {
	if r.Header.Get("Authorization") != "" {
		raw, ok := bearer(r)
		if !ok {
			return core.Actor{}, unauthenticated("send a factory token as Authorization: Bearer")
		}
		return s.authenticateToken(raw)
	}
	ss, err := s.sessionFromRequest(r, !safeMethod(r.Method))
	if err != nil {
		return core.Actor{}, err
	}
	return ss.actor(), nil
}

// sessionFromRequest returns a snapshot of the live session named by the
// cookie, checking the CSRF token when requireCSRF is set. A bearer token is
// refused here: session-only operations are never open to tokens.
func (s *Service) sessionFromRequest(r *http.Request, requireCSRF bool) (session, error) {
	if r.Header.Get("Authorization") != "" {
		return session{}, core.NewError(core.CodePermissionDenied, "this operation needs a browser session; tokens cannot use it")
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return session{}, unauthenticated("sign in first")
	}
	s.mu.Lock()
	ss, ok := s.liveSessionLocked(digest(c.Value))
	var snap session
	if ok {
		snap = *ss
	}
	s.mu.Unlock()
	if !ok {
		return session{}, unauthenticated("the session is unknown, expired or revoked; sign in again")
	}
	if requireCSRF {
		presented := r.Header.Get(CSRFHeader)
		if presented == "" && isForm(r) {
			presented = r.PostFormValue(CSRFFormField)
		}
		if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(snap.csrf)) != 1 {
			return session{}, core.NewError(core.CodePermissionDenied, "a missing or wrong CSRF token")
		}
	}
	return snap, nil
}

// bearer extracts a bearer token.
func bearer(r *http.Request) (string, bool) {
	scheme, raw, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	raw = strings.TrimSpace(raw)
	return raw, ok && strings.EqualFold(scheme, "Bearer") && raw != ""
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func isForm(r *http.Request) bool {
	ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && ct == "application/x-www-form-urlencoded"
}

type actorKey struct{}

// WithActor returns ctx carrying actor.
func WithActor(ctx context.Context, actor core.Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFrom returns the actor Middleware stored in ctx.
func ActorFrom(ctx context.Context) (core.Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(core.Actor)
	return a, ok
}

// Middleware authenticates every request, answers a failure with a typed
// JSON error, and passes the actor, with surface set, to next in the context.
func (s *Service) Middleware(surface core.Surface, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := s.Authenticate(r)
		if err != nil {
			writeError(w, err)
			return
		}
		actor.Surface = surface
		next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), actor)))
	})
}

// cookie builds a cookie with the service's security attributes.
func (s *Service) cookie(r *http.Request, name, value, path string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: path, MaxAge: maxAge, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.cfg.SecureCookies || r.TLS != nil}
}

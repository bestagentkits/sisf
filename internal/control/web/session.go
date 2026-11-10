package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/pkg/core"
)

// sessionPath and logoutPath are the routes of the authentication service
// the dashboard relays to.
const (
	sessionPath = "/auth/session"
	logoutPath  = "/auth/logout"
)

// session is the signed-in person of one request.
type session struct {
	actor core.Actor
	csrf  string
	who   string
	// stepUp are the providers this person can confirm their identity
	// through; empty when none can.
	stepUp []string
}

// sessionView is the part of the session view the dashboard reads.
type sessionView struct {
	Login     string    `json:"login"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Issuer    string    `json:"issuer"`
	CSRFToken string    `json:"csrf_token"`
	StepUpAt  time.Time `json:"step_up_at"`
	// StepUpProviders are the providers that can confirm this person again.
	StepUpProviders []string `json:"step_up_providers"`
}

// identify returns the person behind r. The dashboard is for browser
// sessions: a request that carries an Authorization header is refused, so a
// token never acts through the dashboard and an ambient cookie never rides
// along with one.
func (h *handler) identify(r *http.Request) (*session, error) {
	if r.Header.Get("Authorization") != "" {
		return nil, core.NewError(core.CodeUnauthenticated, "the dashboard needs a browser session; tokens use the API")
	}
	actor, err := h.cfg.Identity.Authenticate(r)
	if err != nil {
		return nil, err
	}
	actor.Surface = core.SurfaceDashboard
	view, err := h.sessionView(r)
	if err != nil {
		return nil, err
	}
	who := firstNonEmpty(view.Name, view.Login, view.Email, actor.ID)
	return &session{actor: actor, csrf: view.CSRFToken, who: who, stepUp: view.StepUpProviders}, nil
}

// sessionView reads the session view from the authentication service, which
// is the one place that knows the CSRF token.
func (h *handler) sessionView(r *http.Request) (sessionView, error) {
	req := httptest.NewRequestWithContext(r.Context(), http.MethodGet, sessionPath, nil)
	for _, c := range r.Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.cfg.Identity.Handler().ServeHTTP(rec, req)
	var view sessionView
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &view) != nil || view.CSRFToken == "" {
		return view, core.NewError(core.CodeUnauthenticated, "sign in first")
	}
	return view, nil
}

// guard serves a page to a signed-in person. A request without a session is
// sent to the sign-in page, and one that fails the CSRF check is refused.
func (h *handler) guard(page func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := h.identify(r)
		if err != nil {
			h.refuse(w, r, err)
			return
		}
		page(w, r, s)
	}
}

// refuse answers a failed identification.
func (h *handler) refuse(w http.ResponseWriter, r *http.Request, err error) {
	if core.HasCode(err, core.CodeUnauthenticated) {
		target := PathSignIn
		if r.Method == http.MethodGet {
			target += "?return_to=" + url.QueryEscape(auth.LocalReturnPath(r.URL.RequestURI()))
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	h.fail(w, r, nil, err)
}

// signIn offers the configured providers. A person who already has a session
// goes on to the page they wanted.
func (h *handler) signIn(w http.ResponseWriter, r *http.Request) {
	returnTo := auth.LocalReturnPath(r.URL.Query().Get("return_to"))
	if _, err := h.identify(r); err == nil {
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
	h.render(w, r, http.StatusOK, pageSignIn, signInView{Providers: h.cfg.LoginProviders, ReturnTo: returnTo})
}

// logout relays the sign-out to the authentication service, which checks the
// CSRF token itself, then returns to the sign-in page.
func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identify(r); err != nil {
		h.refuse(w, r, err)
		return
	}
	relay := r.Clone(r.Context())
	relay.URL = &url.URL{Path: logoutPath}
	relay.RequestURI = logoutPath
	rec := httptest.NewRecorder()
	h.cfg.Identity.Handler().ServeHTTP(rec, relay)
	if rec.Code != http.StatusNoContent {
		h.fail(w, r, nil, core.NewError(core.CodePermissionDenied, "the sign-out was refused"))
		return
	}
	for _, c := range rec.Result().Cookies() {
		http.SetCookie(w, c)
	}
	http.Redirect(w, r, PathSignIn, http.StatusSeeOther)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

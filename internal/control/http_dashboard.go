package control

import (
	"errors"
	"net/http"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/ui"
)

const sessionCookie = "sisf_session"

// pageData is what every dashboard template receives. The pages show the
// structs of the service as they are; nothing is recomputed for display.
type pageData struct {
	Nav      string
	LoggedIn bool
	Message  string
	States   []core.State
	Status   *FactoryStatus
	Issues   *IssueList
	Issue    *IssueDetail
}

func (s *Server) hasSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && s.sessions.valid(c.Value)
}

// requireSession sends a visitor without a live session to the login page.
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hasSession(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// render writes a finished page, or a plain 500 when the template fails.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data pageData) {
	body, err := s.pages.Render(page, data)
	if err != nil {
		s.log.Error("dashboard render failed", "path", r.URL.Path, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	// The client may be gone; there is nobody to tell about a failed write.
	_, _ = w.Write(body)
}

// renderFailure shows a service error on an error page. The message of a
// typed error is written for callers; anything else becomes a fixed text.
func (s *Server) renderFailure(w http.ResponseWriter, r *http.Request, err error) {
	var typed *core.Error
	if !errors.As(err, &typed) || typed.Code == core.CodeInternal {
		s.log.Error("dashboard request failed", "path", r.URL.Path, "error", err)
		typed = core.NewError(core.CodeInternal, "the page could not be built; see the server log")
	}
	s.render(w, r, statusFor(typed.Code), ui.PageError, pageData{LoggedIn: true, Message: typed.Message})
}

func (s *Server) pageOverview(w http.ResponseWriter, r *http.Request) {
	status, err := s.svc.FactoryStatus(r.Context())
	if err != nil {
		s.renderFailure(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, ui.PageOverview, pageData{Nav: "overview", LoggedIn: true, Status: &status})
}

func (s *Server) pageWorkboard(w http.ResponseWriter, r *http.Request) {
	query, err := ParseIssueQuery(r.URL.Query())
	if err != nil {
		s.renderFailure(w, r, err)
		return
	}
	list, err := s.svc.ListIssues(r.Context(), query)
	if err != nil {
		s.renderFailure(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, ui.PageWorkboard,
		pageData{Nav: "workboard", LoggedIn: true, Issues: &list, States: core.States()})
}

func (s *Server) pageIssue(w http.ResponseWriter, r *http.Request) {
	ref, err := core.ParseIssueRef(r.PathValue("id"))
	if err != nil {
		s.renderFailure(w, r, core.NewError(core.CodeInvalidArgument, "the issue id must look like owner/repo#number"))
		return
	}
	detail, err := s.svc.GetIssue(r.Context(), ref)
	if err != nil {
		s.renderFailure(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, ui.PageIssue, pageData{Nav: "workboard", LoggedIn: true, Issue: &detail})
}

func (s *Server) pageNotFound(w http.ResponseWriter, r *http.Request) {
	s.renderFailure(w, r, core.NewError(core.CodeNotFound, "there is no such page"))
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if s.hasSession(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, ui.PageLogin, pageData{})
}

// handleLogin trades the Owner token for a session cookie. The token arrives
// only in the POST body and is never echoed back, stored or logged.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		s.render(w, r, http.StatusBadRequest, ui.PageLogin, pageData{Message: "The login form could not be read."})
		return
	}
	if !s.token.matches(r.PostForm.Get("token")) {
		s.render(w, r, http.StatusUnauthorized, ui.PageLogin, pageData{Message: "That token was not accepted."})
		return
	}
	id, expires, err := s.sessions.create()
	if err != nil {
		s.renderFailure(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: id, Path: "/", Expires: expires,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureRequest(r),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLogout ends the session and clears the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.revoke(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureRequest(r),
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// secureRequest reports whether the browser reached the server over TLS,
// directly or through a reverse proxy that says so. A wrong claim can only
// add the Secure flag to a cookie, which never weakens it.
func secureRequest(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

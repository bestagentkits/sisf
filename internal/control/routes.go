package control

import (
	"fmt"
	"net/http"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/ui"
)

// contentSecurityPolicy lets a page load only its own stylesheet and script
// from this origin. No inline script or style, no external origin, no
// framing and no base tag: even a title that slipped past escaping could not
// run code or reach out.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"img-src 'self'; font-src 'self'; connect-src 'self'; form-action 'self'; " +
	"base-uri 'none'; frame-ancestors 'none'"

// webhookPath is where GitHub delivers webhooks.
const webhookPath = "/webhooks/github"

// PublicPaths are the paths of the control zone that outside traffic may
// reach through the edge. Everything else the control zone serves is for
// the Owner on the factory's own network. The caller owns the slice.
func PublicPaths() []string { return []string{webhookPath, report.Path} }

// routes builds the route table. Only /healthz, the webhook route, the
// report route and the static assets are open; the webhook route proves
// itself with its signature and the report route is rate limited.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle(webhookPath, s.webhook)
	if s.report != nil {
		mux.Handle(report.Path, s.report)
	}
	mux.Handle("GET /static/", ui.StaticHandler())

	mux.HandleFunc("GET /api/v1/factory/status", s.requireOwner(s.apiFactoryStatus))
	mux.HandleFunc("GET /api/v1/issues", s.requireOwner(s.apiListIssues))
	mux.HandleFunc("GET /api/v1/issues/{id}", s.requireOwner(s.apiGetIssue))
	// Any other API path answers like the rest of the API: 401 without the
	// token, a typed 404 with it. A caller learns nothing about routes.
	mux.HandleFunc("/api/", s.requireOwner(s.apiNotFound))

	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /{$}", s.requireSession(s.pageOverview))
	mux.HandleFunc("GET /workboard", s.requireSession(s.pageWorkboard))
	mux.HandleFunc("GET /issues/{id}", s.requireSession(s.pageIssue))
	mux.HandleFunc("/", s.requireSession(s.pageNotFound))

	return s.withSafety(mux)
}

// withSafety wraps every response with the security headers, and turns a
// panic in a handler into a typed 500 instead of a dropped connection.
func (s *Server) withSafety(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("handler panicked", "path", r.URL.Path, "panic", fmt.Sprint(rec))
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

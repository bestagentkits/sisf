package control

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/pkg/core"
)

// contentSecurityPolicy is the default policy of every response. The
// dashboard replaces it with its own, which adds the sign-in provider's
// origin to form-action for a step-up; neither allows inline script or
// style, an external origin, framing or a base tag.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"img-src 'self'; font-src 'self'; connect-src 'self'; form-action 'self'; " +
	"base-uri 'none'; frame-ancestors 'none'"

// webhookPath is where GitHub delivers webhooks.
const webhookPath = "/webhooks/github"

// PublicPaths are the paths of the control zone that outside traffic may
// reach through the edge. Everything else the control zone serves is for
// the Owner on the factory's own network. The caller owns the slice.
func PublicPaths() []string { return []string{webhookPath, report.Path} }

// routes builds the route table. /healthz, the webhook route (absent from a
// server built without webhooks) and the report route are open: the webhook route proves itself with its signature and the
// report route is rate limited. Every other route belongs to a surface, and
// every surface authenticates its caller through the authentication service.
func (s *Server) routes(surfaces Surfaces, clock core.Clock) (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	if s.webhook != nil {
		mux.Handle(webhookPath, s.webhook)
	}
	if s.report != nil {
		mux.Handle(report.Path, s.report)
	}
	if err := surfaces.mount(mux, clock, s.log); err != nil {
		return nil, err
	}
	return s.withSafety(mux), nil
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
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			h.Set("Cache-Control", "no-store")
		}
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("handler panicked", "path", r.URL.Path, "panic", fmt.Sprint(rec))
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

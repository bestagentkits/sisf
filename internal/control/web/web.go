package web

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/ui"
)

// Paths of the dashboard. MountPaths lists the mux patterns that cover them.
const (
	PathOverview  = "/"
	PathWorkboard = "/workboard"
	PathRuns      = "/runs"
	PathTelemetry = "/telemetry"
	PathApprovals = "/approvals"
	PathProduct   = "/product"
	PathSecurity  = "/security"
	PathSetup     = "/setup"
	PathBlocked   = "/blocked"
	PathSignIn    = "/signin"
	PathLogout    = "/logout"
	PathActions   = "/actions/"
	PathStatic    = "/static/"
)

// MountPaths returns the mux patterns the dashboard serves, for a wiring
// package that mounts the handler path by path next to /auth/ and /api/v1/.
// Mounting the handler at "/" serves the same pages.
func MountPaths() []string {
	return []string{"/{$}", PathWorkboard, PathRuns, PathTelemetry, PathApprovals, PathApprovals + "/", PathProduct,
		PathSecurity, PathSetup, PathBlocked, PathSignIn, PathLogout, PathActions, PathStatic}
}

// Actions is the action service; *actions.Service implements it.
type Actions interface {
	Invoke(ctx context.Context, name string, req core.ActionRequest) (core.ActionResult, error)
	Specs() []core.ActionSpec
}

// Identity is the authentication service; *auth.Service implements it. The
// dashboard reads the session through it and never decides who a person is.
type Identity interface {
	// Authenticate returns the actor behind a request, checking the CSRF
	// token of an unsafe method.
	Authenticate(r *http.Request) (core.Actor, error)
	// Handler serves the /auth/ routes; the dashboard reads the session view
	// (its CSRF token) from it and relays a sign-out to it.
	Handler() http.Handler
}

// Config builds the dashboard handler.
type Config struct {
	Actions  Actions
	Identity Identity
	// Clock is the source of time for ages and durations; required.
	Clock core.Clock
	// LoginProviders are the provider names the sign-in page and the
	// step-up buttons offer.
	LoginProviders []string
	// FormOrigins are the origins a form POST may be redirected to besides
	// this server, for the sign-in provider that a step-up leads to. They
	// are added to the form-action directive of the policy.
	FormOrigins []string
	// Logger receives the causes of internal failures; nil discards them.
	Logger *slog.Logger
}

type handler struct {
	cfg    Config
	pages  *ui.Pages
	csp    string
	static http.Handler
	log    *slog.Logger
}

// New returns the dashboard handler. Every response carries the strict
// Content-Security-Policy that ui.ContentSecurityPolicy builds.
func New(cfg Config) (http.Handler, error) {
	if cfg.Actions == nil || cfg.Identity == nil || cfg.Clock == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the dashboard needs the action service, the authentication service and a clock")
	}
	pages, err := ui.LoadDashboard()
	if err != nil {
		return nil, err
	}
	h := &handler{cfg: cfg, pages: pages, csp: ui.ContentSecurityPolicy(cfg.FormOrigins...), static: ui.StaticHandler(), log: cfg.Logger}
	if h.log == nil {
		h.log = slog.New(slog.DiscardHandler)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.guard(h.overview))
	mux.HandleFunc("GET "+PathWorkboard, h.guard(h.workboard))
	mux.HandleFunc("GET "+PathRuns, h.guard(h.runs))
	mux.HandleFunc("GET "+PathTelemetry, h.guard(h.telemetry))
	mux.HandleFunc("GET "+PathApprovals, h.guard(h.approvals))
	mux.HandleFunc("GET "+PathApprovals+"/{id}", h.guard(h.approval))
	mux.HandleFunc("GET "+PathProduct, h.guard(h.product))
	mux.HandleFunc("GET "+PathSecurity, h.guard(h.security))
	mux.HandleFunc("GET "+PathSetup, h.guard(h.setup))
	mux.HandleFunc("GET "+PathBlocked, h.guard(h.blocked))
	mux.HandleFunc("GET "+PathSignIn, h.signIn)
	mux.HandleFunc("POST "+PathLogout, h.logout)
	mux.HandleFunc("POST "+PathActions+"{name}", h.guard(h.postAction))
	mux.Handle("GET "+PathStatic, h.static)
	mux.HandleFunc("/", h.notFound)
	return h.withHeaders(mux), nil
}

// withHeaders adds the security headers to every response. Pages are never
// cached: they show live state and a CSRF token.
func (h *handler) withHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", h.csp)
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Referrer-Policy", "no-referrer")
		if !strings.HasPrefix(r.URL.Path, PathStatic) {
			hd.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

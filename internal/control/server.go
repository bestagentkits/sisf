package control

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/ui"
)

// Config is the static configuration of the control server.
type Config struct {
	FactoryID core.FactoryID
	// ListenAddr is where Serve listens. Empty means DefaultListenAddr.
	ListenAddr string
	// AllowNonLoopback permits a listen address that other hosts can reach.
	// It is off by default; turn it on only behind TLS and a firewall.
	AllowNonLoopback bool
	// OwnerToken is the bootstrap Owner token generated at install. It is
	// never logged, rendered or returned.
	OwnerToken core.SecretValue
	// Webhook configures the GitHub receiver behind the webhook route. Its
	// Clock defaults to the server's clock.
	Webhook github.ReceiverConfig
	// ProbeTimeout bounds one zone health probe. Zero means DefaultProbeTimeout.
	ProbeTimeout time.Duration
	// SessionTTL is the dashboard login lifetime. Zero means DefaultSessionTTL.
	SessionTTL time.Duration
}

// Deps are the collaborators of the server, all behind interfaces so the
// factory binary and the in-process test harness can wire their own.
type Deps struct {
	// Store is the control zone's operational store. It must be opened for
	// the control zone: only that zone may store a webhook delivery.
	Store state.Store
	// Probes check Zone 1 and Zone 2. A zone without a probe reports unknown.
	Probes map[core.Zone]HealthProbe
	// Clock defaults to the system clock.
	Clock core.Clock
	// Logger receives operational messages and never a token or a payload.
	// Nil discards them.
	Logger *slog.Logger
	// Report, when set, serves the public bug report route.
	Report *ReportRoute
}

// Server is the control HTTP server. Handler gives the routes for tests and
// for embedding; Serve runs them on a listener.
type Server struct {
	cfg      Config
	svc      *Service
	token    *ownerToken
	sessions *sessions
	pages    *ui.Pages
	webhook  *github.Receiver
	report   http.Handler
	log      *slog.Logger
	handler  http.Handler
}

// New validates the configuration and builds the server. It refuses to build
// when the listen address is reachable from other hosts without an explicit
// allowance, when the token is too short, or when the webhook receiver cannot
// make its checks.
func New(cfg Config, deps Deps) (*Server, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = DefaultListenAddr
	}
	if err := validateListenAddr(cfg.ListenAddr, cfg.AllowNonLoopback); err != nil {
		return nil, err
	}
	token, err := newOwnerToken(cfg.OwnerToken)
	if err != nil {
		return nil, err
	}
	if deps.Clock == nil {
		deps.Clock = core.SystemClock{}
	}
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = DefaultSessionTTL
	}
	svc, err := NewService(cfg.FactoryID, deps.Store, deps.Probes, cfg.ProbeTimeout, deps.Clock)
	if err != nil {
		return nil, err
	}
	pages, err := ui.Load()
	if err != nil {
		return nil, err
	}
	if cfg.Webhook.Clock == nil {
		cfg.Webhook.Clock = deps.Clock
	}
	receiver, err := github.NewReceiver(cfg.Webhook, newInboxSink(deps.Store, deps.Logger))
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg: cfg, svc: svc, token: token, sessions: newSessions(deps.Clock, cfg.SessionTTL),
		pages: pages, webhook: receiver, log: deps.Logger,
	}
	if deps.Report != nil {
		if s.report, err = deps.Report.handler(deps.Clock); err != nil {
			return nil, err
		}
	}
	s.handler = s.routes()
	return s, nil
}

// Service returns the read service both the API and the dashboard render from.
func (s *Server) Service() *Service { return s.svc }

// Handler returns the complete HTTP handler, security headers included.
func (s *Server) Handler() http.Handler { return s.handler }

// Serve serves on ln until ctx ends, then shuts down gracefully. A listener
// bound to an address other hosts can reach is refused unless the
// configuration allows it, so a caller cannot bypass the loopback rule by
// passing its own socket. Serve returns nil after a clean shutdown.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if err := validateListener(ln, s.cfg.AllowNonLoopback); err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    32 << 10,
	}
	failed := make(chan error, 1)
	go func() { failed <- srv.Serve(ln) }()
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-failed; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// ListenAndServe listens on the configured address and serves until ctx ends.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.cfg.ListenAddr)
	if err != nil {
		return core.NewError(core.CodeUnavailable, "the control server could not listen on its address").WithCause(err)
	}
	return s.Serve(ctx, ln)
}

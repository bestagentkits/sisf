package wiring

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"time"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state"
)

// DispatcherConfig is what the agent zone is built from.
type DispatcherConfig struct {
	// Manifest supplies the run limits, which hold in every budget mode.
	Manifest *manifest.Manifest
	// Store is the operational store opened for the agent zone.
	Store state.Store
	Agent agent.Backend
	// Owner names this instance in its leases; a fresh DispatcherOwner when
	// empty.
	Owner        string
	Clock        core.Clock
	PollInterval time.Duration
	LeaseTTL     time.Duration
	// NewRunID names runs; random when nil.
	NewRunID func() core.RunID
	// Estimate is what each run sets aside from a capped budget; a capped
	// budget admits no run without one. Zero suits an unlimited budget.
	Estimate core.Micros
	// OnTick follows every tick of the dispatcher's loop, such as
	// DispatcherTickLog; nothing when nil.
	OnTick func(dispatcher.TickResult, error)
}

// DispatcherOwner names one dispatcher process: its host and a random
// suffix of its own. Two processes on one host, or two containers that share
// a host name, are therefore two lease holders, and only one of them is the
// active dispatcher at a time.
//
// A restarted process has a new name, so it cannot take its predecessor's
// leases back at once. A process that stops cleanly gives its leases up and
// its successor takes over at its next tick. One that was killed holds them
// until they lapse, at most the lease lifetime (DispatcherLeaseTTL) after
// its last tick; the successor then takes them under new fencing tokens,
// which fence off whatever the killed process left in flight.
func DispatcherOwner() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "localhost"
	}
	var suffix [4]byte
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(suffix[:])
	return "dispatcher@" + host + "#" + hex.EncodeToString(suffix[:])
}

// LimitsOf reads the run limits of the manifest.
func LimitsOf(m *manifest.Manifest) dispatcher.Limits {
	return dispatcher.Limits{RunTimeout: m.Limits.RunTimeout.Std(), MaxAttempts: m.Limits.MaxAttempts, MaxConcurrent: m.Limits.Concurrency}
}

// NewDispatcher builds the agent zone's dispatcher: the budget gate that
// admits no run before the Owner confirmed the budget and holds a capped
// budget to its cap, briefs read from what intake approved, and the
// manifest's run limits.
func NewDispatcher(cfg DispatcherConfig) (*dispatcher.Dispatcher, error) {
	if cfg.Manifest == nil || cfg.Store == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the dispatcher needs the manifest and a state store")
	}
	if cfg.Owner == "" {
		cfg.Owner = DispatcherOwner()
	}
	gate := budget.NewGate(budget.Config{Confirmations: dispatcher.KVConfirmation{}, Clock: cfg.Clock})
	return dispatcher.New(dispatcher.Config{
		Owner: cfg.Owner, State: cfg.Store, Agent: cfg.Agent, Budget: gate, Briefs: StoreBriefs(cfg.Store),
		Limits: LimitsOf(cfg.Manifest), Clock: cfg.Clock, LeaseTTL: cfg.LeaseTTL, PollInterval: cfg.PollInterval,
		NewRunID: cfg.NewRunID, Estimate: cfg.Estimate, OnTick: cfg.OnTick,
	})
}

// DispatcherHealth serves GET /healthz: the dispatcher's own snapshot, with
// 200 while it ticks and 503 once it has not ticked for staleAfter. The
// snapshot names runs and fencing tokens, which are not secret: a token
// proves nothing without the lease holder's name and a live lease.
func DispatcherHealth(d *dispatcher.Dispatcher, clock core.Clock, staleAfter time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		health := d.Health()
		code := http.StatusOK
		if health.LastTick.IsZero() || clock.Now().Sub(health.LastTick) > staleAfter {
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(health)
	})
	return mux
}

// Listen opens addr, refusing an address other hosts can reach unless
// allowNonLoopback says so.
func Listen(ctx context.Context, addr string, allowNonLoopback bool) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the listen address must be host:port")
	}
	if ip, perr := netip.ParseAddr(host); !allowNonLoopback && host != "localhost" && (perr != nil || !ip.IsLoopback()) {
		return nil, core.NewError(core.CodePermissionDenied, "the listen address is not on loopback; set "+EnvAllowNonLoopback+" to expose it")
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, core.NewError(core.CodeUnavailable, "cannot listen on "+addr).WithCause(err)
	}
	return ln, nil
}

// Serve runs handler on ln until ctx ends, then shuts it down.
func Serve(ctx context.Context, ln net.Listener, handler http.Handler) error {
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	failed := make(chan error, 1)
	go func() { failed <- srv.Serve(ln) }()
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return err
	}
	if err := <-failed; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

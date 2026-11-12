package egress

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// DefaultAddress is where the proxy listens when the wiring names no
// address: the port runners' HTTP_PROXY and HTTPS_PROXY point at.
const DefaultAddress = ":3128"

// PolicySource returns the egress policy in force. The proxy asks for it on
// every connection, so an approved change takes effect at once; an error
// blocks the connection.
type PolicySource interface {
	EgressPolicy(ctx context.Context) (core.EgressPolicy, error)
}

// StaticPolicy is a policy that never changes, for tests and for wiring
// that holds the policy itself.
type StaticPolicy core.EgressPolicy

// EgressPolicy returns the policy.
func (s StaticPolicy) EgressPolicy(context.Context) (core.EgressPolicy, error) {
	return core.EgressPolicy(s), nil
}

// Resolver looks up the addresses of a host; *net.Resolver is one.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DialFunc opens a connection to an address the proxy already checked.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Config is what a proxy is built from. Only Policy is required.
type Config struct {
	Policy PolicySource
	// Resolver resolves host names; net.DefaultResolver when nil.
	Resolver Resolver
	// Dial connects to a checked address. When nil the proxy dials with a
	// dialer that checks the socket's address once more before connecting.
	Dial DialFunc
	// Recorder keeps blocked destinations; a MemoryRecorder when nil.
	Recorder Recorder
	// Logger receives one line per blocked attempt; slog.Default when nil.
	Logger *slog.Logger
	// Clock stamps blocked attempts; the wall clock when nil.
	Clock core.Clock
	// ConnectPorts are the ports CONNECT may reach; 443 when empty.
	ConnectPorts []uint16
	// HTTPPorts are the ports plain HTTP may reach; 80 when empty.
	HTTPPorts []uint16
	// HeaderTimeout bounds how long a client may take to send its request
	// head; ten seconds when zero.
	HeaderTimeout time.Duration
}

// Proxy is the egress proxy. It is safe for concurrent use.
type Proxy struct {
	cfg Config

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

// New builds a proxy.
func New(cfg Config) (*Proxy, error) {
	if cfg.Policy == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the egress proxy needs the policy in force")
	}
	if cfg.Resolver == nil {
		cfg.Resolver = net.DefaultResolver
	}
	if cfg.Dial == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second, Control: guardSocket}
		cfg.Dial = dialer.DialContext
	}
	if cfg.Recorder == nil {
		cfg.Recorder = NewMemoryRecorder(0)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	if len(cfg.ConnectPorts) == 0 {
		cfg.ConnectPorts = []uint16{443}
	}
	if len(cfg.HTTPPorts) == 0 {
		cfg.HTTPPorts = []uint16{80}
	}
	if cfg.HeaderTimeout == 0 {
		cfg.HeaderTimeout = 10 * time.Second
	}
	return &Proxy{cfg: cfg, conns: map[net.Conn]struct{}{}}, nil
}

// Recorder returns where blocked destinations are kept.
func (p *Proxy) Recorder() Recorder { return p.cfg.Recorder }

// Capabilities is what the proxy lets an execution backend declare: that
// the egress policy is enforced for every sandbox whose only way out is
// this proxy.
func (p *Proxy) Capabilities() core.CapabilitySet {
	return core.CapabilitySet{core.CapExecEgressEnforcement: core.SupportSupported}
}

// ListenAndServe listens on addr (DefaultAddress when empty) and serves
// until ctx ends.
func (p *Proxy) ListenAndServe(ctx context.Context, addr string) error {
	if addr == "" {
		addr = DefaultAddress
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return core.NewError(core.CodeUnavailable, "the egress proxy cannot listen on "+addr).WithCause(err)
	}
	return p.Serve(ctx, ln)
}

// Serve accepts connections on ln until ctx ends, then closes ln and every
// connection still open, tunnels included.
func (p *Proxy) Serve(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() {
		_ = ln.Close()
		p.mu.Lock()
		defer p.mu.Unlock()
		for c := range p.conns {
			_ = c.Close()
		}
	})
	defer stop()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		p.track(c, true)
		go func() {
			defer p.track(c, false)
			defer func() { _ = c.Close() }()
			if ctx.Err() == nil { // accepted while shutting down
				p.handle(ctx, c)
			}
		}()
	}
}

// track adds or removes an open connection.
func (p *Proxy) track(c net.Conn, open bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if open {
		p.conns[c] = struct{}{}
	} else {
		delete(p.conns, c)
	}
}

// portAllowed reports whether port is in ports.
func portAllowed(ports []uint16, port uint16) bool { return slices.Contains(ports, port) }

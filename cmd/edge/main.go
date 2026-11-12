// Command edge is the reverse proxy in front of the application. It sends
// every request to --upstream, except the paths named by --route, which go
// to the upstream of that route.
//
//	edge --listen :7460 --upstream http://app:7461 \
//	     --route /webhooks/github=http://control:7463
//
// The default upstream is the stable revision, labelled by --revision. A
// canary is added and removed through the admin listener (--admin-listen,
// guarded by the token in EDGE_ADMIN_TOKEN), which also serves the
// per-revision counters a release monitor reads. EDGE_COUNTERS_TOKEN, when
// set, is a second token that reads the counters and nothing else; it must
// differ from the admin token, or the edge refuses to start.
//
// The egress proxy of the agent sandbox listens next to it on
// --egress-listen (default 127.0.0.1:7466, empty turns it off) with the
// default allowlist plus the hosts of each --egress-provider.
//
// Every listener is on loopback unless told otherwise, and any other
// address needs --allow-non-loopback, because the program has no TLS and
// no access control of its own beyond the admin token.
//
// Exit status: 0 after a clean shutdown, 1 when the proxy fails, 2 when it
// was invoked wrongly.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/bestagentkits/sisf/cmd/supervisor/observe"
	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Exit statuses of the program.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// Listen addresses of the fixed port table, on loopback.
const (
	defaultListen       = "127.0.0.1:7460"
	defaultEgressListen = "127.0.0.1:7466"
)

// adminTokenEnv names the variable that carries the admin bearer token, and
// countsTokenEnv the read-only counters token, so neither token ever
// appears in a process listing.
const (
	adminTokenEnv  = "EDGE_ADMIN_TOKEN"
	countsTokenEnv = "EDGE_COUNTERS_TOKEN"
)

func main() {
	// A Standalone install hands each unit its run lock on descriptor 3 so the
	// install counts as running while the unit lives. Nothing this unit starts
	// (an agent, a build, the application) may hold that lock after it.
	syscall.CloseOnExec(3)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	status := run(ctx, os.Args[1:], os.Stderr)
	stop()
	os.Exit(status)
}

// routeFlags collects repeated --route flags.
type routeFlags []edge.Route

func (r *routeFlags) String() string { return fmt.Sprint(len(*r)) + " routes" }
func (r *routeFlags) Set(spec string) error {
	route, err := edge.ParseRoute(spec)
	if err != nil {
		return err
	}
	*r = append(*r, route)
	return nil
}

// listFlags collects a repeated string flag.
type listFlags []string

func (l *listFlags) String() string     { return strings.Join(*l, ",") }
func (l *listFlags) Set(v string) error { *l = append(*l, v); return nil }

// run parses the command line, builds the listeners and serves until ctx ends.
func run(ctx context.Context, args []string, stderr io.Writer) int {
	var (
		opts     options
		routes   routeFlags
		upstream string
	)
	flags := flag.NewFlagSet("edge", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.listen, "listen", defaultListen, "address to listen on")
	flags.StringVar(&upstream, "upstream", "", "default upstream, the stable revision, http://host:port (required)")
	flags.StringVar(&opts.revision, "revision", "stable", "label of the stable revision on responses and counters")
	flags.StringVar(&opts.adminListen, "admin-listen", "", "address of the admin listener for the traffic split and counters; off when empty")
	flags.StringVar(&opts.egressListen, "egress-listen", defaultEgressListen, "address of the egress proxy; off when empty")
	flags.Var(&opts.providers, "egress-provider", "model provider whose API hosts the egress allowlist adds, repeatable")
	flags.BoolVar(&opts.allowPublic, "allow-non-loopback", false, "allow a listen address other hosts can reach")
	flags.Var(&routes, "route", "/prefix=http://host:port, repeatable")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	usage := func(format string, a ...any) int {
		_, _ = fmt.Fprintf(stderr, "edge: "+format+"\n", a...)
		return exitUsage
	}
	if flags.NArg() > 0 {
		return usage("unexpected argument %q", flags.Arg(0))
	}
	def, err := edge.ParseUpstream(upstream)
	if err != nil {
		return usage("--upstream: %v", err)
	}
	opts.stable, opts.routes = edge.Target{Revision: opts.revision, URL: def.String()}, routes
	opts.adminToken, opts.countsToken = os.Getenv(adminTokenEnv), os.Getenv(countsTokenEnv)
	var extra []string
	if opts.countsToken != "" {
		extra = append(extra, opts.countsToken)
	}
	proc, err := observe.Start(stderr, os.Getenv, core.SystemClock{}, extra...)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "edge: %v\n", err)
		return exitFailure
	}
	defer func() { _ = proc.Shutdown(context.WithoutCancel(ctx)) }()
	log := proc.Log
	srv, err := newServers(ctx, opts, log)
	if err != nil {
		var u usageError
		if errors.As(err, &u) {
			return usage("%v", err)
		}
		_, _ = fmt.Fprintf(stderr, "edge: %v\n", err)
		return exitFailure
	}
	if err := srv.serve(ctx, log); err != nil {
		_, _ = fmt.Fprintf(stderr, "edge: %v\n", err)
		return exitFailure
	}
	return exitOK
}

// loopbackAddr reports whether addr is host:port with a loopback host. An
// empty or wildcard host listens on every interface and is not loopback.
func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

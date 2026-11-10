// Command edge is the reverse proxy in front of the application. It sends
// every request to --upstream, except the paths named by --route, which go
// to the upstream of that route.
//
//	edge --listen :7460 --upstream http://app:7461 \
//	     --route /webhooks/github=http://control:7463
//
// It listens on the loopback address 127.0.0.1:7460 unless told otherwise,
// and any other address needs --allow-non-loopback, because the program has
// no TLS and no access control of its own.
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
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/bestagentkits/sisf/internal/edge"
)

// Exit statuses of the program.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// defaultListen is the edge port of the fixed port table, on loopback.
const defaultListen = "127.0.0.1:7460"

func main() {
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

// run parses the command line, builds the proxy and serves until ctx ends.
func run(ctx context.Context, args []string, stderr io.Writer) int {
	var (
		listen, upstream string
		allowPublic      bool
		routes           routeFlags
	)
	flags := flag.NewFlagSet("edge", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&listen, "listen", defaultListen, "address to listen on")
	flags.StringVar(&upstream, "upstream", "", "default upstream, http://host:port (required)")
	flags.BoolVar(&allowPublic, "allow-non-loopback", false, "allow a listen address other hosts can reach")
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
	if !allowPublic && !loopbackAddr(listen) {
		return usage("--listen %q is reachable from other hosts; pass --allow-non-loopback to serve it", listen)
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	handler, err := edge.New(edge.Config{Default: def, Routes: routes, Logger: log})
	if err != nil {
		return usage("%v", err)
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", listen)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "edge: cannot listen on %s: %v\n", listen, err)
		return exitFailure
	}
	log.Info("edge serving", "listen", ln.Addr().String(), "routes", len(routes))
	if err := edge.Serve(ctx, ln, handler); err != nil {
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

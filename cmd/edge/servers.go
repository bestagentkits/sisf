package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/internal/egress"
	"github.com/bestagentkits/sisf/pkg/core"
)

// usageError marks a failure that is in the invocation, not in the system.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// options is everything the listeners are built from.
type options struct {
	listen, adminListen, egressListen string
	revision, adminToken, countsToken string
	allowPublic                       bool
	providers                         listFlags
	stable                            edge.Target
	routes                            []edge.Route
}

// servers are the open listeners and what serves each of them. The admin and
// egress listeners are nil when they are switched off.
type servers struct {
	publicLn, adminLn, egressLn net.Listener
	public, admin               http.Handler
	proxy                       *egress.Proxy
}

// newServers validates the options, opens the listeners and builds what
// serves them. Nothing is left open on failure.
func newServers(ctx context.Context, opts options, log *slog.Logger) (_ *servers, err error) {
	for name, addr := range map[string]string{"--listen": opts.listen, "--admin-listen": opts.adminListen, "--egress-listen": opts.egressListen} {
		if addr != "" && !opts.allowPublic && !loopbackAddr(addr) {
			return nil, usagef("%s %q is reachable from other hosts; pass --allow-non-loopback to serve it", name, addr)
		}
	}
	if opts.adminListen != "" && opts.adminToken == "" {
		return nil, usagef("--admin-listen needs the token in %s", adminTokenEnv)
	}
	if opts.countsToken != "" && opts.countsToken == opts.adminToken {
		return nil, usagef("%s must differ from %s, so reading the counters never grants moving traffic", countsTokenEnv, adminTokenEnv)
	}
	splitter, err := edge.NewSplitter(core.SystemClock{}, log, opts.stable)
	if err != nil {
		return nil, usagef("%v", err)
	}
	s := &servers{}
	if s.public, err = edge.New(edge.Config{Split: splitter, Routes: opts.routes, Logger: log}); err != nil {
		return nil, usagef("%v", err)
	}
	if opts.adminListen != "" {
		var counts []edge.AdminOption
		if opts.countsToken != "" {
			counts = append(counts, edge.WithCountsToken(opts.countsToken))
		}
		if s.admin, err = edge.NewAdminHandler(splitter, opts.adminToken, counts...); err != nil {
			return nil, usagef("%v", err)
		}
	}
	if opts.egressListen != "" {
		policy, err := egress.DefaultPolicy(opts.providers...)
		if err != nil {
			return nil, usagef("--egress-provider: %v", err)
		}
		if s.proxy, err = egress.New(egress.Config{Policy: egress.StaticPolicy(policy), Logger: log}); err != nil {
			return nil, usagef("egress proxy: %v", err)
		}
	}
	defer func() {
		if err != nil {
			s.close()
		}
	}()
	for _, l := range []struct {
		ln   *net.Listener
		addr string
	}{{&s.publicLn, opts.listen}, {&s.adminLn, opts.adminListen}, {&s.egressLn, opts.egressListen}} {
		if l.addr == "" {
			continue
		}
		if *l.ln, err = (&net.ListenConfig{}).Listen(ctx, "tcp", l.addr); err != nil {
			return nil, fmt.Errorf("cannot listen on %s: %w", l.addr, err)
		}
	}
	return s, nil
}

func (s *servers) close() {
	for _, ln := range []net.Listener{s.publicLn, s.adminLn, s.egressLn} {
		if ln != nil {
			_ = ln.Close()
		}
	}
}

// serve runs every listener until ctx ends or one of them fails, and then
// stops the others.
func (s *servers) serve(ctx context.Context, log *slog.Logger) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	log.Info("edge serving", "listen", s.publicLn.Addr().String(), "admin", s.adminLn != nil, "egress", s.egressLn != nil)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	run := func(f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f(); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
			stop()
		}()
	}
	run(func() error { return edge.Serve(ctx, s.publicLn, s.public) })
	if s.adminLn != nil {
		run(func() error { return edge.Serve(ctx, s.adminLn, s.admin) })
	}
	if s.egressLn != nil {
		run(func() error { return s.proxy.Serve(ctx, s.egressLn) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

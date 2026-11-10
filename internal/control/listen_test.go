package control_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

func TestServerBindsToLoopbackByDefault(t *testing.T) {
	t.Parallel()
	if control.DefaultListenAddr != "127.0.0.1:7463" {
		t.Fatalf("default address = %q, want 127.0.0.1:7463", control.DefaultListenAddr)
	}
}

func TestNonLoopbackAddressIsRefusedUnlessAllowed(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		base := newEnv(t, db)
		cases := map[string]bool{
			"127.0.0.1:7463":     true,
			"127.0.0.2:7463":     true,
			"[::1]:7463":         true,
			"localhost:7463":     true,
			"0.0.0.0:7463":       false,
			":7463":              false,
			"[::]:7463":          false,
			"192.168.1.20:7463":  false,
			"10.0.0.5:7463":      false,
			"control.example:80": false,
		}
		for addr, loopback := range cases {
			cfg, deps := base.cfg, base.deps
			cfg.ListenAddr = addr
			_, err := control.New(cfg, deps)
			if loopback && err != nil {
				t.Errorf("%s: refused: %v", addr, err)
			}
			if !loopback && !core.HasCode(err, core.CodePermissionDenied) {
				t.Errorf("%s: error = %v, want PERMISSION_DENIED", addr, err)
			}
			cfg.AllowNonLoopback = true
			if _, err := control.New(cfg, deps); err != nil {
				t.Errorf("%s with allow_non_loopback: %v", addr, err)
			}
		}
	})
}

func TestMalformedListenAddressIsRefused(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		base := newEnv(t, db)
		for _, addr := range []string{"7463", "127.0.0.1", "not an address"} {
			cfg := base.cfg
			cfg.ListenAddr = addr
			if _, err := control.New(cfg, base.deps); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("%q: error = %v, want INVALID_ARGUMENT", addr, err)
			}
		}
	})
}

func TestServeRefusesAWildcardListenerUnlessAllowed(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e := newEnv(t, db)
		wildcard, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			t.Skipf("cannot open a wildcard socket here: %v", err)
		}
		t.Cleanup(func() { _ = wildcard.Close() })
		// A cancelled context ends a server that wrongly accepted the socket,
		// so a regression fails this test instead of hanging it.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := e.server.Serve(ctx, wildcard); !core.HasCode(err, core.CodePermissionDenied) {
			t.Fatalf("Serve on a wildcard listener = %v, want PERMISSION_DENIED", err)
		}
	})
}

func TestServeAnswersOnLoopbackAndStopsWhenAsked(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e := newEnv(t, db)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ctx, stop := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- e.server.Serve(ctx, ln) }()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ln.Addr().String()+"/api/v1/issues", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 over a real socket", resp.StatusCode)
		}

		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned %v after a clean stop", err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("Serve did not stop")
		}
	})
}

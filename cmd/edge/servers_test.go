package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/edge"
)

const testToken = "admin-token-for-tests"

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func baseOptions(t *testing.T) options {
	t.Helper()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "app") }))
	t.Cleanup(app.Close)
	return options{
		listen: "127.0.0.1:0", adminListen: "127.0.0.1:0", egressListen: "127.0.0.1:0",
		adminToken: testToken, stable: edge.Target{Revision: "rev-1", URL: app.URL},
	}
}

// start opens the servers and serves them until the test ends.
func start(t *testing.T, opts options) *servers {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := newServers(ctx, opts, quietLog())
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.serve(ctx, quietLog()) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve after cancel: %v", err)
		}
	})
	return srv
}

func TestThePublicListenerServesTheStableWithItsRevisionLabel(t *testing.T) {
	t.Parallel()
	srv := start(t, baseOptions(t))
	resp, err := http.Get("http://" + srv.publicLn.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "app" || resp.Header.Get(edge.RevisionHeader) != "rev-1" {
		t.Fatalf("answer = %q with revision %q, want app from rev-1", body, resp.Header.Get(edge.RevisionHeader))
	}
}

func TestTheAdminSurfaceLivesOnItsOwnListenerOnly(t *testing.T) {
	t.Parallel()
	srv := start(t, baseOptions(t))
	for name, base := range map[string]string{"public": "http://" + srv.publicLn.Addr().String(), "admin": "http://" + srv.adminLn.Addr().String()} {
		resp, err := http.Get(base + "/split")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		want := http.StatusUnauthorized // the admin listener wants its token
		if name == "public" {
			want = http.StatusOK // the public path goes to the app, which answers anything
		}
		if resp.StatusCode != want {
			t.Errorf("%s listener: GET /split = %d, want %d", name, resp.StatusCode, want)
		}
	}
}

// The control zone reads the counters with a token of its own that cannot
// move traffic.
func TestTheCountersTokenReadsTheCountersButCannotChangeTheSplit(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t)
	opts.countsToken = "counts-token-for-tests"
	srv := start(t, opts)
	base := "http://" + srv.adminLn.Addr().String()
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/counters", http.StatusOK},
		{http.MethodGet, "/split", http.StatusForbidden},
		{http.MethodPut, "/split", http.StatusForbidden},
	} {
		req, _ := http.NewRequest(c.method, base+c.path, strings.NewReader(`{"stable":{"revision":"x","url":"http://127.0.0.1:9"}}`))
		req.Header.Set("Authorization", "Bearer "+opts.countsToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s %s with the counters token = %d, want %d", c.method, c.path, resp.StatusCode, c.want)
		}
	}
}

func TestTheEgressListenerBlocksAHostOffTheAllowlist(t *testing.T) {
	t.Parallel()
	srv := start(t, baseOptions(t))
	conn, err := net.Dial("tcp", srv.egressLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = fmt.Fprint(conn, "CONNECT not-on-the-list.example:443 HTTP/1.1\r\nHost: not-on-the-list.example:443\r\n\r\n")
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.Contains(status, "403") {
		t.Fatalf("CONNECT to an unlisted host answered %q (%v), want 403", status, err)
	}
}

func TestEgressAndAdminCanBeSwitchedOff(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t)
	opts.adminListen, opts.egressListen = "", ""
	srv := start(t, opts)
	if srv.adminLn != nil || srv.egressLn != nil {
		t.Fatal("a switched-off listener was opened")
	}
}

func TestListenersOffLoopbackAndAnOpenAdminSurfaceAreRefused(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*options){
		"public egress":     func(o *options) { o.egressListen = ":7466" },
		"public admin":      func(o *options) { o.adminListen = "0.0.0.0:7470" },
		"public edge":       func(o *options) { o.listen = ":7460" },
		"admin without key": func(o *options) { o.adminToken = "" },
		"equal tokens":      func(o *options) { o.countsToken = o.adminToken },
		"unknown provider":  func(o *options) { o.providers = listFlags{"no-such-provider"} },
	} {
		opts := baseOptions(t)
		mutate(&opts)
		_, err := newServers(context.Background(), opts, quietLog())
		var u usageError
		if !errors.As(err, &u) {
			t.Errorf("%s: err = %v, want a usage error", name, err)
		}
	}
}

func TestAPublicAddressIsAllowedWhenAskedForExplicitly(t *testing.T) {
	t.Parallel()
	opts := baseOptions(t)
	opts.listen, opts.egressListen, opts.allowPublic = "0.0.0.0:0", "0.0.0.0:0", true
	start(t, opts)
}

func TestAFailedListenLeavesNothingOpen(t *testing.T) {
	t.Parallel()
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()
	opts := baseOptions(t)
	opts.egressListen = taken.Addr().String()
	if _, err := newServers(context.Background(), opts, quietLog()); err == nil {
		t.Fatal("listening on a taken address succeeded")
	}
}

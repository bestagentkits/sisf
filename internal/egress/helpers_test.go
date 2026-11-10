package egress_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/egress"
	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

var start = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

// publicTestAddr is a public documentation address the fake resolver hands
// out for hosts that may be reached; the test dialer leads it to a local
// listener.
const publicTestAddr = "203.0.113.10"

// network is a fake outside world: a resolver, and a dialer that leads
// checked public addresses to local listeners and records every dial.
type network struct {
	mu     sync.Mutex
	hosts  map[string][]netip.Addr
	routes map[string]string
	dialed []string
}

func newNetwork() *network {
	return &network{hosts: map[string][]netip.Addr{}, routes: map[string]string{}}
}

func (n *network) resolve(host string, addrs ...string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, a := range addrs {
		n.hosts[host] = append(n.hosts[host], netip.MustParseAddr(a))
	}
}

// route leads connections to addr (ip:port) to the local listener at target.
func (n *network) route(addr, target string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.routes[addr] = target
}

func (n *network) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	addrs, ok := n.hosts[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return append([]netip.Addr(nil), addrs...), nil
}

func (n *network) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	n.mu.Lock()
	n.dialed = append(n.dialed, addr)
	target, ok := n.routes[addr]
	n.mu.Unlock()
	if !ok {
		return nil, errors.New("unreachable in the test network")
	}
	var d net.Dialer
	return d.DialContext(ctx, network, target)
}

func (n *network) dials() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.dialed...)
}

// startProxy serves a proxy over the fake network on a random loopback port.
func startProxy(t *testing.T, policy egress.PolicySource, n *network) (string, *egress.MemoryRecorder) {
	t.Helper()
	recorder := egress.NewMemoryRecorder(100)
	p, err := egress.New(egress.Config{Policy: policy, Resolver: n, Dial: n.dial, Recorder: recorder,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Clock: intaketest.NewClock(start)})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("the proxy stopped with %v", err)
		}
	})
	return ln.Addr().String(), recorder
}

// send writes a raw request to the proxy and returns the response and the
// connection, which stays open for a tunnel.
func send(t *testing.T, proxy, request string) (*http.Response, net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", proxy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: strings.Fields(request)[0]})
	if err != nil {
		t.Fatalf("no response to %q: %v", strings.SplitN(request, "\r\n", 2)[0], err)
	}
	return resp, conn, br
}

// status sends a request and returns only the response status.
func status(t *testing.T, proxy, request string) int {
	t.Helper()
	resp, _, _ := send(t, proxy, request)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func connect(target, host string) string {
	req := "CONNECT " + target + " HTTP/1.1\r\n"
	if host != "" {
		req += "Host: " + host + "\r\n"
	}
	return req + "\r\n"
}

func get(url, host string) string {
	return "GET " + url + " HTTP/1.1\r\nHost: " + host + "\r\nConnection: close\r\n\r\n"
}

// echoServer accepts connections and echoes what it reads.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func wantCode(t *testing.T, err error, code core.ErrorCode, what string) {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("%s: want %s, got %v", what, code, err)
	}
}

// approve records the Owner's approval of a request.
func approve(t *testing.T, store *approvals.KVStore, owner core.Actor, r approvals.Request) {
	t.Helper()
	_, err := store.Submit(context.Background(), owner, approvals.Submission{RequestID: r.ID, Nonce: r.Nonce,
		SubjectDigest: r.SubjectDigest, PolicyRevision: r.PolicyRevision, Decision: approvals.DecisionApprove})
	if err != nil {
		t.Fatalf("the Owner's approval was not recorded: %v", err)
	}
}

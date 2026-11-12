package egress_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/egress"
	"github.com/bestagentkits/sisf/pkg/core"
)

// bypassEnv is a proxy whose allowlist holds the test hosts, over a fake
// network where allowed.test is public and every other test host leads
// somewhere a runner must never reach.
type bypassEnv struct {
	proxy string
	net   *network
}

func newBypassEnv(t *testing.T, mode core.EgressMode) bypassEnv {
	t.Helper()
	n := newNetwork()
	n.resolve("allowed.test", publicTestAddr)
	n.resolve("loopback.allowed.test", "127.0.0.1")
	n.resolve("private.allowed.test", "10.0.0.5")
	n.resolve("metadata.allowed.test", "169.254.169.254")
	n.resolve("mapped.allowed.test", "::ffff:127.0.0.1")
	n.resolve("cgnat.allowed.test", "100.100.100.200")
	n.resolve("ula.allowed.test", "fd00:ec2::254")
	n.resolve("v6loop.allowed.test", "::1")
	n.resolve("mixed.allowed.test", publicTestAddr, "10.0.0.1")
	n.resolve("nat64.allowed.test", "64:ff9b::7f00:1")
	n.route(publicTestAddr+":443", echoServer(t))
	policy := core.EgressPolicy{Mode: mode, Allow: []string{"allowed.test", "*.allowed.test"}}
	proxy, _ := startProxy(t, egress.StaticPolicy(policy), n)
	return bypassEnv{proxy: proxy, net: n}
}

func TestAnAllowedTunnelCarriesBytesBothWays(t *testing.T) {
	t.Parallel()
	e := newBypassEnv(t, core.EgressAllowlist)
	resp, conn, br := send(t, e.proxy, connect("allowed.test:443", "allowed.test:443"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT to an allowed host: status %d", resp.StatusCode)
	}
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("the tunnel did not carry the bytes: %q %v", buf, err)
	}
}

func TestIPLiteralsAndDisguisedAddressesAreRefused(t *testing.T) {
	t.Parallel()
	e := newBypassEnv(t, core.EgressAllowlist)
	for _, target := range []string{publicTestAddr + ":443", "127.0.0.1:443", "[::1]:443", "169.254.169.254:443",
		"[::ffff:127.0.0.1]:443", "[fe80::1%25eth0]:443", "2130706433:443", "0x7f000001:443", "127.1:443", "0177.0.0.1:443",
		"10.0.0.1.:443", "allowed.test.127.0.0.1:443"} {
		if got := status(t, e.proxy, connect(target, "")); got < 400 {
			t.Errorf("CONNECT %s: status %d", target, got)
		}
	}
	for _, url := range []string{"http://169.254.169.254/latest/meta-data/", "http://[fd00:ec2::254]/", "http://2852039166/"} {
		if got := status(t, e.proxy, get(url, strings.TrimSuffix(strings.TrimPrefix(url, "http://"), "/latest/meta-data/"))); got < 400 {
			t.Errorf("GET %s: status %d", url, got)
		}
	}
	if d := e.net.dials(); len(d) != 0 {
		t.Fatalf("refused destinations were dialed: %v", d)
	}
}

func TestOnlyTheAllowedPortsCanBeReached(t *testing.T) {
	t.Parallel()
	e := newBypassEnv(t, core.EgressUnrestricted)
	for _, target := range []string{"allowed.test:22", "allowed.test:80", "allowed.test:8443", "allowed.test:0443", "allowed.test:65536", "allowed.test:"} {
		if got := status(t, e.proxy, connect(target, "")); got < 400 {
			t.Errorf("CONNECT %s: status %d", target, got)
		}
	}
	for _, url := range []string{"http://allowed.test:8080/", "http://allowed.test:443/", "http://allowed.test:22/"} {
		if got := status(t, e.proxy, get(url, strings.TrimSuffix(strings.TrimPrefix(url, "http://"), "/"))); got < 400 {
			t.Errorf("GET %s: status %d", url, got)
		}
	}
	if d := e.net.dials(); len(d) != 0 {
		t.Fatalf("refused ports were dialed: %v", d)
	}
}

// A name on the list that resolves into the host, a private network or a
// metadata service is refused, also with blocking turned off.
func TestNamesResolvingToNonPublicAddressesAreRefused(t *testing.T) {
	t.Parallel()
	for _, mode := range []core.EgressMode{core.EgressAllowlist, core.EgressUnrestricted} {
		e := newBypassEnv(t, mode)
		for _, host := range []string{"loopback", "private", "metadata", "mapped", "cgnat", "ula", "v6loop", "mixed", "nat64"} {
			if got := status(t, e.proxy, connect(host+".allowed.test:443", "")); got != http.StatusForbidden {
				t.Errorf("%s: CONNECT %s.allowed.test: status %d", mode, host, got)
			}
		}
		if got := status(t, e.proxy, get("http://metadata.allowed.test/latest/meta-data/", "metadata.allowed.test")); got != http.StatusForbidden {
			t.Errorf("%s: GET through a name for the metadata address: status %d", mode, got)
		}
		if d := e.net.dials(); len(d) != 0 {
			t.Fatalf("%s: non-public addresses were dialed: %v", mode, d)
		}
	}
}

func TestAHostHeaderThatDisagreesWithTheTargetIsRefused(t *testing.T) {
	t.Parallel()
	e := newBypassEnv(t, core.EgressAllowlist)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	t.Cleanup(upstream.Close)
	e.net.route(publicTestAddr+":80", strings.TrimPrefix(upstream.URL, "http://"))
	for name, req := range map[string]string{
		"connect host header":       connect("allowed.test:443", "attacker.example:443"),
		"connect host header port":  connect("allowed.test:443", "allowed.test:8443"),
		"connect two host headers":  "CONNECT allowed.test:443 HTTP/1.1\r\nHost: allowed.test:443\r\nHost: attacker.example:443\r\n\r\n",
		"http host header":          get("http://allowed.test/", "attacker.example"),
		"http host header port":     get("http://allowed.test/", "allowed.test:8080"),
		"http two host headers":     "GET http://allowed.test/ HTTP/1.1\r\nHost: allowed.test\r\nHost: attacker.example\r\n\r\n",
		"origin form with host":     get("/", "allowed.test"),
		"https through plain proxy": get("https://allowed.test/", "allowed.test"),
		"credentials in url":        get("http://user:pass@allowed.test/", "allowed.test"),
	} {
		if got := status(t, e.proxy, req); got < 400 {
			t.Errorf("%s: status %d", name, got)
		}
	}
	if got := status(t, e.proxy, get("http://allowed.test/", "allowed.test")); got != http.StatusOK {
		t.Fatalf("a well-formed request to an allowed host: status %d", got)
	}
}

// The proxy hands a redirect back instead of following it, so the next hop
// is checked like any other request.
func TestRedirectsAreNotFollowed(t *testing.T) {
	t.Parallel()
	e := newBypassEnv(t, core.EgressAllowlist)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://attacker.example/collect", http.StatusFound)
	}))
	t.Cleanup(upstream.Close)
	e.net.route(publicTestAddr+":80", strings.TrimPrefix(upstream.URL, "http://"))
	resp, _, _ := send(t, e.proxy, get("http://allowed.test/start", "allowed.test"))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "http://attacker.example/collect" {
		t.Fatalf("the redirect was not handed back: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := status(t, e.proxy, get("http://attacker.example/collect", "attacker.example")); got != http.StatusForbidden {
		t.Fatalf("following the redirect: status %d", got)
	}
	for _, d := range e.net.dials() {
		if d != publicTestAddr+":80" {
			t.Fatalf("the proxy dialed %s", d)
		}
	}
}

func TestAnUnavailablePolicyBlocksEverything(t *testing.T) {
	t.Parallel()
	n := newNetwork()
	n.resolve("allowed.test", publicTestAddr)
	proxy, _ := startProxy(t, failingPolicy{}, n)
	if got := status(t, proxy, connect("allowed.test:443", "")); got != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", got)
	}
	if got := status(t, proxy, "NOT A REQUEST\r\n\r\n"); got != http.StatusBadRequest {
		t.Fatalf("garbage: status %d", got)
	}
}

type failingPolicy struct{}

func (failingPolicy) EgressPolicy(context.Context) (core.EgressPolicy, error) {
	return core.EgressPolicy{}, errors.New("store down")
}

func TestTheProxyStopsWithItsContext(t *testing.T) {
	t.Parallel()
	p, err := egress.New(egress.Config{Policy: egress.StaticPolicy{Mode: core.EgressAllowlist}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Capabilities().Level(core.CapExecEgressEnforcement) != core.SupportSupported {
		t.Fatal("the proxy does not publish egress enforcement")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Serve(ctx, ln) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned %v", err)
	}
	if _, err := egress.New(egress.Config{}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a proxy without a policy was built: %v", err)
	}
}

package edge_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bestagentkits/sisf/internal/edge"
)

// upstream is a fake backend that records the last request it served.
type upstream struct {
	*httptest.Server
	hits atomic.Int32
	path atomic.Value // string
	xff  atomic.Value // string
}

func newUpstream(t *testing.T, name string) *upstream {
	t.Helper()
	u := &upstream{}
	u.path.Store("")
	u.xff.Store("")
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		u.path.Store(r.URL.Path)
		u.xff.Store(r.Header.Get("X-Forwarded-For"))
		_, _ = io.WriteString(w, name)
	}))
	t.Cleanup(u.Close)
	return u
}

func get(t *testing.T, h http.Handler, target string, headers ...string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func mustRoute(t *testing.T, spec string) edge.Route {
	t.Helper()
	r, err := edge.ParseRoute(spec)
	if err != nil {
		t.Fatalf("ParseRoute(%q): %v", spec, err)
	}
	return r
}

func newEdge(t *testing.T, app, control *upstream) http.Handler {
	t.Helper()
	def, err := edge.ParseUpstream(app.URL)
	if err != nil {
		t.Fatal(err)
	}
	h, err := edge.New(edge.Config{
		Default: def,
		Routes:  []edge.Route{mustRoute(t, "/webhooks/github="+control.URL), mustRoute(t, "/report="+control.URL)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestTrafficGoesToTheDefaultUpstreamUnlessARouteMatches(t *testing.T) {
	t.Parallel()
	app, control := newUpstream(t, "app"), newUpstream(t, "control")
	h := newEdge(t, app, control)

	for _, tc := range []struct{ path, want string }{
		{"/", "app"}, {"/healthz", "app"}, {"/webhooks/github", "control"},
		{"/report", "control"}, {"/report/submit", "control"},
	} {
		if code, body := get(t, h, tc.path); code != http.StatusOK || body != tc.want {
			t.Errorf("GET %s = %d %q, want 200 %q", tc.path, code, body, tc.want)
		}
	}
}

func TestARouteMatchesWholePathSegmentsOnly(t *testing.T) {
	t.Parallel()
	app, control := newUpstream(t, "app"), newUpstream(t, "control")
	h := newEdge(t, app, control)

	// "/reports" and "/webhooks/github-admin" only share a string prefix with
	// a public route; they must not reach the control plane.
	for _, path := range []string{"/reports", "/webhooks/github-admin", "/webhooks"} {
		if _, body := get(t, h, path); body != "app" {
			t.Errorf("GET %s reached %q, want the default upstream", path, body)
		}
	}
	if control.hits.Load() != 0 {
		t.Fatalf("control served %d requests for paths outside its routes", control.hits.Load())
	}
}

func TestDotSegmentsCannotSteerARequestIntoARoute(t *testing.T) {
	t.Parallel()
	app, control := newUpstream(t, "app"), newUpstream(t, "control")
	h := newEdge(t, app, control)

	// The decision is made on the cleaned path, and the cleaned path is what
	// is forwarded, so the upstream never sees a ".." it could resolve.
	if _, body := get(t, h, "/report/../api/v1/factory/status"); body != "app" {
		t.Fatalf("a path that leaves /report reached %q, want the default upstream", body)
	}
	if got := app.path.Load().(string); got != "/api/v1/factory/status" {
		t.Fatalf("app saw path %q, want the cleaned path", got)
	}
	if control.hits.Load() != 0 {
		t.Fatal("control served a request that left its route")
	}
}

func TestClientForwardedForHeadersAreReplacedNotTrusted(t *testing.T) {
	t.Parallel()
	app, control := newUpstream(t, "app"), newUpstream(t, "control")
	h := newEdge(t, app, control)

	get(t, h, "/", "X-Forwarded-For", "6.6.6.6")
	if got := app.xff.Load().(string); strings.Contains(got, "6.6.6.6") {
		t.Fatalf("upstream received the client-supplied forwarded address: %q", got)
	}
}

func TestAnUnreachableUpstreamIsABadGatewayWithoutLeakingTheAddress(t *testing.T) {
	t.Parallel()
	app, control := newUpstream(t, "app"), newUpstream(t, "control")
	h := newEdge(t, app, control)
	control.Close()

	code, body := get(t, h, "/report")
	if code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", code)
	}
	if strings.Contains(body, "127.0.0.1") {
		t.Fatalf("error body leaks the upstream address: %q", body)
	}
}

func TestParseRouteRefusesMalformedRoutes(t *testing.T) {
	t.Parallel()
	for name, spec := range map[string]string{
		"no separator":        "/report",
		"prefix not absolute": "report=http://control:7463",
		"empty prefix":        "=http://control:7463",
		"trailing slash":      "/report/=http://control:7463",
		"dot segment":         "/a/../b=http://control:7463",
		"upstream with path":  "/report=http://control:7463/inner",
		"upstream with creds": "/report=http://user:pw@control:7463",
		"unsupported scheme":  "/report=ftp://control:7463",
		"no host":             "/report=http://",
		"query in prefix":     "/report?x=1=http://control:7463",
	} {
		if _, err := edge.ParseRoute(spec); err == nil {
			t.Errorf("%s: ParseRoute(%q) succeeded", name, spec)
		}
	}
}

func TestNewRefusesAnIncompleteConfiguration(t *testing.T) {
	t.Parallel()
	def, _ := edge.ParseUpstream("http://app:7461")
	route := mustRoute(t, "/report=http://control:7463")
	if _, err := edge.New(edge.Config{}); err == nil {
		t.Error("a configuration without a default upstream was accepted")
	}
	if _, err := edge.New(edge.Config{Default: def, Routes: []edge.Route{route, route}}); err == nil {
		t.Error("two routes for the same prefix were accepted")
	}
}

func TestServeProxiesUntilItsContextEnds(t *testing.T) {
	t.Parallel()
	app, control := newUpstream(t, "app"), newUpstream(t, "control")
	h := newEdge(t, app, control)
	// The listener is open before Serve starts, so the request below queues
	// on it and no waiting for readiness is needed.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- edge.Serve(ctx, ln, h) }()

	resp, err := testClient.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "app" {
		t.Fatalf("body = %q, want app", body)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve after cancel: %v", err)
	}
}

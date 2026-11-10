package docker_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/exec/docker"
	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/internal/dockerfilter/dockerfiltertest"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

const (
	factory = "acme-app"
	network = "acme-app_sandbox"
	proxy   = "http://edge:3128"
)

var (
	start = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	image = "ghcr.io/acme/sisf-runner@sha256:" + strings.Repeat("c", 64)
)

// clock is a test clock that also reports each timer a command sets, so a
// test moves time only once the backend is waiting on it.
type clock struct {
	*sqlstoretest.Clock
	timers chan time.Duration
}

func newClock() clock {
	return clock{Clock: sqlstoretest.NewClock(start), timers: make(chan time.Duration, 16)}
}

func (c clock) After(d time.Duration) <-chan time.Time {
	ch := c.Clock.After(d)
	c.timers <- d
	return ch
}

// env is a backend talking through a real filter to a fake daemon.
type env struct {
	backend *docker.Backend
	daemon  *dockerfiltertest.Daemon
	clock   clock
}

type options struct {
	runtimes []string
	cfg      func(*docker.Config)
	// rewrite changes what the daemon answers to inspect requests.
	rewrite func(view map[string]any)
	// externalNetwork makes the sandbox network an ordinary, non-internal one.
	externalNetwork bool
	// noNetwork leaves the daemon without the sandbox network.
	noNetwork bool
}

func newEnv(t *testing.T, o options) env {
	t.Helper()
	if o.runtimes == nil {
		o.runtimes = []string{"runc", "runsc"}
	}
	daemon := dockerfiltertest.New(o.runtimes...)
	if !o.noNetwork {
		daemon.AddNetwork(network, !o.externalNetwork)
	}
	var handler http.Handler = daemon
	if o.rewrite != nil {
		handler = rewriteInspect(daemon, o.rewrite)
	}
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	filter, err := dockerfilter.New(dockerfilter.Config{Upstream: upstream.Client().Transport, UpstreamURL: upstream.URL,
		RunnerValue: factory, SandboxNetwork: network, Images: []string{image}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(filter)
	t.Cleanup(front.Close)
	c := newClock()
	cfg := docker.Config{Endpoint: front.URL, FactoryID: factory, Network: network, EgressProxy: proxy, Clock: c}
	if o.cfg != nil {
		o.cfg(&cfg)
	}
	b, err := docker.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return env{backend: b, daemon: daemon, clock: c}
}

// rewriteInspect lets a test make the daemon misreport a container.
func rewriteInspect(daemon http.Handler, rewrite func(map[string]any)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/json") {
			daemon.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		daemon.ServeHTTP(rec, r)
		var view map[string]any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &view) != nil {
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		if labels, _ := view["Config"].(map[string]any)["Labels"].(map[string]any); labels["sisf.runner"] == factory {
			rewrite(view)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(view)
	})
}

func spec(run core.RunID, trust execution.Trust) execution.SandboxSpec {
	return execution.SandboxSpec{
		RunID: run, Unit: "runner", Image: image, Trust: trust,
		Egress: core.EgressPolicy{Mode: core.EgressAllowlist, Allow: []string{"github.com"}},
		Limits: execution.Limits{Timeout: 30 * time.Minute, CPUMillis: 1000, MemoryBytes: 1 << 30, PIDs: 256},
		Env:    map[string]string{"SISF_RUN": string(run)},
	}
}

func (e env) create(t *testing.T, s execution.SandboxSpec) execution.SandboxInfo {
	t.Helper()
	info, err := e.backend.CreateSandbox(context.Background(), s)
	if err != nil {
		t.Fatalf("the sandbox was not created: %v", err)
	}
	return info
}

// createdBodies returns the configurations the daemon received.
func (e env) createdBodies(t *testing.T) []dockerfilter.CreateRequest {
	t.Helper()
	var out []dockerfilter.CreateRequest
	for _, r := range e.daemon.Created() {
		var req dockerfilter.CreateRequest
		if err := json.Unmarshal(r.Body, &req); err != nil {
			t.Fatal(err)
		}
		out = append(out, req)
	}
	return out
}

func wantCode(t *testing.T, err error, code core.ErrorCode, what string) {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("%s: want %s, got %v", what, code, err)
	}
}

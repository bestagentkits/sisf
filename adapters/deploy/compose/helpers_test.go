package compose_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/deploy/compose"
	"github.com/bestagentkits/sisf/internal/dockerfilter/dockerfiltertest"
	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

const factory = core.FactoryID("acme-app")

var (
	digestA = core.ReleaseDigest("sha256:" + strings.Repeat("a", 64))
	digestB = core.ReleaseDigest("sha256:" + strings.Repeat("b", 64))
	digestC = core.ReleaseDigest("sha256:" + strings.Repeat("c", 64))
	epoch   = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
)

// stepClock moves only when something waits on it: After advances the
// clock by the wait and fires at once, so a poll loop runs without sleeping.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

// world is a fake Docker daemon, the applications that run in its
// containers, and a real in-process edge in front of them.
type world struct {
	t        *testing.T
	daemon   *dockerfiltertest.Daemon
	docker   *httptest.Server
	splitter *edge.Splitter
	edgeH    http.Handler
	backend  *compose.Backend
	clock    *stepClock

	mu sync.Mutex
	// say overrides the revision an application reports, by container name.
	say map[string]string
	// down makes an application answer 503, by container name.
	down map[string]bool
	// failPage makes an application's page answer 500, by container name.
	failPage map[string]bool
	// labelLie rewrites the digest label that inspect returns, by container name.
	labelLie map[string]string
	// logs is what a container writes to stderr, by container name.
	logs map[string]string
	apps map[string]*httptest.Server
}

type worldOptions struct {
	edge    func(edge.Controller) edge.Controller
	cfg     func(*compose.Config)
	timeout time.Duration
}

func newWorld(t *testing.T, o worldOptions) *world {
	t.Helper()
	w := &world{
		t: t, daemon: dockerfiltertest.New("runc"), clock: &stepClock{now: epoch},
		say: map[string]string{}, down: map[string]bool{}, failPage: map[string]bool{},
		labelLie: map[string]string{}, logs: map[string]string{}, apps: map[string]*httptest.Server{},
	}
	w.daemon.OnStart(func(c dockerfiltertest.Container) dockerfiltertest.Outcome {
		w.mu.Lock()
		defer w.mu.Unlock()
		return dockerfiltertest.Outcome{Hang: true, Stderr: w.logs[c.Name]}
	})
	w.docker = httptest.NewServer(http.HandlerFunc(w.serveDocker))
	t.Cleanup(w.docker.Close)

	var err error
	// The stable starts as something that is not one of ours.
	if w.splitter, err = edge.NewSplitter(w.clock, nil, edge.Target{Revision: "bootstrap", URL: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	if w.edgeH, err = edge.New(edge.Config{Split: w.splitter}); err != nil {
		t.Fatal(err)
	}
	var ctl edge.Controller = w.splitter
	if o.edge != nil {
		ctl = o.edge(ctl)
	}
	cfg := compose.Config{
		Endpoint: w.docker.URL, Factory: factory, Network: "acme-app_default", AppPort: 7461,
		Env: []string{"HELLO_DATA_DIR=/data"}, Volumes: []compose.VolumeMount{{Name: "acme-app_data", Target: "/data"}},
		Edge: ctl, Resolve: w.resolve, Clock: w.clock, ReadyTimeout: o.timeout, PollInterval: time.Second,
	}
	if o.cfg != nil {
		o.cfg(&cfg)
	}
	if w.backend, err = compose.New(cfg); err != nil {
		t.Fatal(err)
	}
	return w
}

// serveDocker is the daemon plus the container list endpoint, with the
// ability to make inspect lie about the digest label.
func (w *world) serveDocker(rw http.ResponseWriter, r *http.Request) {
	path := r.URL.Path[strings.Index(r.URL.Path, "/containers"):]
	switch {
	case r.Method == http.MethodGet && path == "/containers/json":
		w.list(rw, r)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/json"):
		rec := httptest.NewRecorder()
		w.daemon.ServeHTTP(rec, r)
		body := rec.Body.Bytes()
		name := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
		w.mu.Lock()
		lie, lying := w.labelLie[name]
		w.mu.Unlock()
		if lying && rec.Code == http.StatusOK {
			var view map[string]any
			_ = json.Unmarshal(body, &view)
			view["Config"].(map[string]any)["Labels"].(map[string]any)[compose.LabelDigest] = lie
			body, _ = json.Marshal(view)
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(rec.Code)
		_, _ = rw.Write(body)
	default:
		w.daemon.ServeHTTP(rw, r)
	}
}

func (w *world) list(rw http.ResponseWriter, r *http.Request) {
	var filters map[string][]string
	_ = json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters)
	var out []map[string]any
	for _, c := range w.daemon.Containers() {
		match := true
		for _, l := range filters["label"] {
			k, v, _ := strings.Cut(l, "=")
			match = match && c.Labels[k] == v
		}
		if !match {
			continue
		}
		state := "exited"
		if c.Running {
			state = "running"
		}
		out = append(out, map[string]any{"Names": []string{"/" + c.Name}, "Labels": c.Labels, "State": state})
	}
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(out)
}

// resolve returns the address of the application inside a container,
// starting its fake on first use.
func (w *world) resolve(name string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if srv := w.apps[name]; srv != nil {
		return srv.URL
	}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { w.app(name, rw, r) }))
	w.t.Cleanup(srv.Close)
	w.apps[name] = srv
	return srv.URL
}

// app is the application in container name: it reports the revision the
// container was started as, unless a test makes it say otherwise.
func (w *world) app(name string, rw http.ResponseWriter, r *http.Request) {
	revision := ""
	running := false
	for _, c := range w.daemon.Containers() {
		if c.Name != name {
			continue
		}
		running = c.Running
		for _, e := range c.Env {
			if v, ok := strings.CutPrefix(e, compose.RevisionEnv+"="); ok {
				revision = v
			}
		}
	}
	w.mu.Lock()
	if said, ok := w.say[name]; ok {
		revision = said
	}
	down, failPage := w.down[name], w.failPage[name]
	w.mu.Unlock()
	if !running {
		http.Error(rw, "not running", http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/healthz" {
		code, status := http.StatusOK, "ok"
		if down {
			code, status = http.StatusServiceUnavailable, "down"
		}
		rw.WriteHeader(code)
		_, _ = fmt.Fprintf(rw, `{"status":%q,"revision":%q}`, status, revision)
		return
	}
	if failPage {
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, _ = io.WriteString(rw, "ok")
}

// name is the container name of a revision, as the backend derives it.
func name(digest core.ReleaseDigest) string {
	return string(factory) + "-app-" + strings.TrimPrefix(string(digest), "sha256:")[:12]
}

func artifact(digest core.ReleaseDigest) deploy.Artifact {
	return deploy.Artifact{
		FactoryID: factory, Unit: "app", Digest: digest,
		Image:     "ghcr.io/acme/app@" + string(digest),
		Signature: core.Signature{KeyID: "k1", Algorithm: "ed25519", Value: []byte("sig")},
	}
}

func (w *world) stage(digest core.ReleaseDigest) deploy.Revision {
	w.t.Helper()
	rev, err := w.backend.Stage(context.Background(), artifact(digest))
	if err != nil {
		w.t.Fatalf("Stage(%s): %v", name(digest), err)
	}
	return rev
}

// release stages a revision and makes it the stable one.
func (w *world) release(digest core.ReleaseDigest) deploy.Revision {
	w.t.Helper()
	rev := w.stage(digest)
	if err := w.backend.Promote(context.Background(), rev, 100); err != nil {
		w.t.Fatalf("Promote(%s, 100): %v", name(digest), err)
	}
	return rev
}

// traffic sends n requests through the edge and returns the revision
// headers seen.
func (w *world) traffic(n int) map[string]int {
	seen := map[string]int{}
	for range n {
		rec := httptest.NewRecorder()
		w.edgeH.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		seen[rec.Header().Get(edge.RevisionHeader)]++
	}
	return seen
}

func (w *world) containerNames() []string {
	var out []string
	for _, c := range w.daemon.Containers() {
		out = append(out, c.Name)
	}
	return out
}

func (w *world) running(name string) bool {
	for _, c := range w.daemon.Containers() {
		if c.Name == name {
			return c.Running
		}
	}
	return false
}

// brokenEdge is an edge that cannot be reached.
type brokenEdge struct{}

func (brokenEdge) Split(context.Context) (edge.Split, error) {
	return edge.Split{}, core.NewError(core.CodeUnavailable, "edge down")
}
func (brokenEdge) SetSplit(context.Context, edge.Split) error {
	return core.NewError(core.CodeUnavailable, "edge down")
}
func (brokenEdge) Counts(context.Context, time.Duration) (map[string]edge.Counts, error) {
	return nil, core.NewError(core.CodeUnavailable, "edge down")
}

// countsDown is an edge whose split works but whose counters do not.
type countsDown struct{ edge.Controller }

func (countsDown) Counts(context.Context, time.Duration) (map[string]edge.Counts, error) {
	return nil, core.NewError(core.CodeUnavailable, "counters down")
}

func edgeSplit(revision, url string) edge.Split {
	return edge.Split{Stable: edge.Target{Revision: revision, URL: url}}
}

// stopContainer stops a container through the daemon API, as an operator would.
func (w *world) stopContainer(name string) error {
	req, _ := http.NewRequest(http.MethodPost, w.docker.URL+"/v1.43/containers/"+name+"/stop", nil)
	resp, err := testClient.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (w *world) setSay(name, revision string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.say[name] = revision
}

func (w *world) setDown(name string, down bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.down[name] = down
}

func (w *world) setFailPage(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failPage[name] = true
}

func (w *world) setLabelLie(name, digest string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.labelLie[name] = digest
}

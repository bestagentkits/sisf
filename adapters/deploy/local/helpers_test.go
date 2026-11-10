package local_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/deploy/local"
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
// clock by the wait and fires at once.
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

// fakeStarter stands in for the operating system: each slot has a server
// that is always listening, and "starting a process" on the slot makes it
// answer as the application. A stopped process makes the server drop the
// connection, as a closed port would. The ports are held for the whole test,
// so no other test or program can take one in between.
type fakeStarter struct {
	mu       sync.Mutex
	say      map[string]string // revision an application reports, by address
	down     map[string]bool   // applications that answer 503, by address
	failPage map[string]bool
	procs    map[string]*fakeProc // the latest process of each address
	servers  map[string]*httptest.Server
	started  int
}

type fakeProc struct {
	mu       sync.Mutex
	alive    bool
	revision string
	critical int64
}

func (p *fakeProc) Alive() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.alive }
func (p *fakeProc) Stop(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.alive = false
	return nil
}
func (p *fakeProc) CriticalSince(time.Time) int64 { return p.critical }

// newFakeStarter opens the two slot servers and returns the starter and the ports.
func newFakeStarter(t *testing.T) (*fakeStarter, [2]int) {
	t.Helper()
	s := &fakeStarter{
		say: map[string]string{}, down: map[string]bool{}, failPage: map[string]bool{},
		procs: map[string]*fakeProc{}, servers: map[string]*httptest.Server{},
	}
	var ports [2]int
	for i := range ports {
		var addr string
		srv := httptest.NewUnstartedServer(nil)
		srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.serve(addr, w, r) })
		srv.Start()
		t.Cleanup(srv.Close)
		addr = srv.Listener.Addr().String()
		ports[i] = srv.Listener.Addr().(*net.TCPAddr).Port
		s.servers[addr] = srv
	}
	return s, ports
}

func (s *fakeStarter) serve(addr string, w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	proc := s.procs[addr]
	said, lies := s.say[addr]
	down, failPage := s.down[addr], s.failPage[addr]
	s.mu.Unlock()
	if proc == nil || !proc.Alive() {
		panic(http.ErrAbortHandler) // nothing is listening: the connection is dropped
	}
	reported := proc.revision
	if lies {
		reported = said
	}
	if r.URL.Path == "/healthz" {
		code, status := http.StatusOK, "ok"
		if down {
			code, status = http.StatusServiceUnavailable, "down"
		}
		w.WriteHeader(code)
		_, _ = fmt.Fprintf(w, `{"status":%q,"revision":%q}`, status, reported)
		return
	}
	if failPage {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (s *fakeStarter) Start(_ context.Context, spec local.StartSpec) (local.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.servers[spec.Addr] == nil {
		return nil, fmt.Errorf("no slot listens on %s", spec.Addr)
	}
	s.started++
	p := &fakeProc{alive: true}
	for _, e := range spec.Env {
		if v, ok := strings.CutPrefix(e, local.RevisionEnv+"="); ok {
			p.revision = v
		}
	}
	s.procs[spec.Addr] = p
	return p, nil
}

func (s *fakeStarter) set(m map[string]bool, addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m[addr] = true
}

type world struct {
	t        *testing.T
	starter  *fakeStarter
	splitter *edge.Splitter
	edgeH    http.Handler
	backend  *local.Backend
	clock    *stepClock
	ports    [2]int
}

func (w *world) addr(slot int) string { return fmt.Sprintf("127.0.0.1:%d", w.ports[slot]) }

type worldOptions struct {
	edge    func(edge.Controller) edge.Controller
	cfg     func(*local.Config)
	timeout time.Duration
}

func newWorld(t *testing.T, o worldOptions) *world {
	t.Helper()
	starter, ports := newFakeStarter(t)
	w := &world{t: t, clock: &stepClock{now: epoch}, ports: ports, starter: starter}
	var err error
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
	cfg := local.Config{Factory: factory, Ports: ports, Starter: starter, Edge: ctl, Clock: w.clock, ReadyTimeout: o.timeout, PollInterval: time.Second}
	if o.cfg != nil {
		o.cfg(&cfg)
	}
	if w.backend, err = local.New(cfg); err != nil {
		t.Fatal(err)
	}
	return w
}

func artifact(digest core.ReleaseDigest) deploy.Artifact {
	return deploy.Artifact{
		FactoryID: factory, Unit: "app", Digest: digest, Image: "bin/app@" + string(digest),
		Signature: core.Signature{KeyID: "k1", Algorithm: "ed25519", Value: []byte("sig")},
	}
}

func (w *world) stage(digest core.ReleaseDigest) deploy.Revision {
	w.t.Helper()
	rev, err := w.backend.Stage(context.Background(), artifact(digest))
	if err != nil {
		w.t.Fatalf("Stage: %v", err)
	}
	return rev
}

func (w *world) release(digest core.ReleaseDigest) deploy.Revision {
	w.t.Helper()
	rev := w.stage(digest)
	if err := w.backend.Promote(context.Background(), rev, 100); err != nil {
		w.t.Fatalf("Promote: %v", err)
	}
	return rev
}

func (w *world) traffic(n int) map[string]int {
	seen := map[string]int{}
	for range n {
		rec := httptest.NewRecorder()
		w.edgeH.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		seen[rec.Header().Get(edge.RevisionHeader)]++
	}
	return seen
}

func (w *world) alive(slot int) bool {
	p := w.starter.procs[w.addr(slot)]
	return p != nil && p.Alive()
}

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

type countsDown struct{ edge.Controller }

func (countsDown) Counts(context.Context, time.Duration) (map[string]edge.Counts, error) {
	return nil, core.NewError(core.CodeUnavailable, "counters down")
}

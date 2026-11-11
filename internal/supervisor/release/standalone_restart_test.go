package release

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/deploy/local"
	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// appEnv makes the test binary run as the application of a slot: it
// answers its health check as healthy, naming the revision it was started
// as, on the address it was given, until it is stopped.
const (
	appEnv     = "SISF_RELEASE_TEST_APP"
	appAddrEnv = "SISF_LISTEN"
)

func TestMain(m *testing.M) {
	if os.Getenv(appEnv) == "1" {
		serveTestApp()
		return
	}
	os.Exit(m.Run())
}

// serveTestApp is the application the real local backend starts in these
// tests. It writes nothing to its standard error, whose reader goes away
// with the backend that started it.
func serveTestApp() {
	revision := os.Getenv(local.RevisionEnv)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "revision": revision})
	})
	srv := &http.Server{Addr: os.Getenv(appAddrEnv), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		os.Exit(1)
	}
}

// startedProcesses remembers every process a starter started, so the test
// stops them all whatever backend lost track of them.
type startedProcesses struct {
	local.Starter
	mu    sync.Mutex
	procs []local.Process
}

func (s *startedProcesses) Start(ctx context.Context, spec local.StartSpec) (local.Process, error) {
	p, err := s.Starter.Start(ctx, spec)
	if err == nil {
		s.mu.Lock()
		s.procs = append(s.procs, p)
		s.mu.Unlock()
	}
	return p, err
}

func (s *startedProcesses) stopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.procs {
		_ = p.Stop(context.Background())
	}
}

// freePorts returns two ports nothing listens on now, outside the ports a
// running factory uses.
func freePorts(t *testing.T) [2]int {
	t.Helper()
	var ports [2]int
	for i := range ports {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ports[i] = l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
	}
	return ports
}

// standaloneBackend is the real local backend over the edge, recording its
// slots in dir, with real processes of the test application.
func standaloneBackend(t *testing.T, ports [2]int, ctl edge.Controller, dir string, starter local.Starter) *local.Backend {
	t.Helper()
	b, err := local.New(local.Config{Factory: testFactory, Ports: ports, Starter: starter, Edge: ctl, StateDir: dir,
		ReadyTimeout: 20 * time.Second, PollInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// slotPID is the process the record of slot names.
func slotPID(t *testing.T, dir, slot string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, slot+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	return rec.PID
}

// Standalone: the supervisor stops between the Verified step of a release
// and the write of its LKG, and starts again while the release's process
// and the edge run on. The restarted backend finds the slots it recorded,
// so at startup the release is observed serving, healthy, and recorded as
// the LKG before the LKG is adopted, and it is finished: the process that
// serves is neither stopped nor replaced by the release before it.
func TestAStandaloneSupervisorRestartedBetweenTheVerifiedStepAndTheLKGKeepsTheRelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Lander = &countingLander{}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	starter := &startedProcesses{Starter: local.ExecStarter{
		AddrEnv: appAddrEnv, Env: []string{appEnv + "=1"},
		Command: func(string) (string, []string, error) { return exe, []string{"-test.run=^$"}, nil },
	}}
	t.Cleanup(starter.stopAll)
	splitter, err := edge.NewSplitter(core.SystemClock{}, nil, edge.Target{Revision: "bootstrap", URL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	ports, dir := freePorts(t), filepath.Join(t.TempDir(), "local-slots")

	// The first supervisor adopts the LKG, then releases the next build.
	first := standaloneBackend(t, ports, splitter, dir, starter)
	for _, digest := range []core.ReleaseDigest{lkgDigest, core.ReleaseDigest(firstBuild)} {
		rev, err := first.Stage(ctx, w.keys.artifactFor(t, core.Digest(digest)))
		if err != nil {
			t.Fatalf("stage %s: %v", digest, err)
		}
		if err := first.Promote(ctx, rev, 100); err != nil {
			t.Fatalf("promote %s: %v", digest, err)
		}
	}
	issue, run := issueRef(260), core.RunID("run-260")
	w.seedVerified(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	serving, err := first.ObservedRevision(ctx, testUnit)
	if err != nil || !serving.Matches(deploy.Revision{Unit: testUnit, Digest: core.ReleaseDigest(firstBuild)}) {
		t.Fatalf("before the restart %+v serves (%v), want the verified release", serving, err)
	}
	pid := slotPID(t, dir, "green")

	// It stops before it writes the LKG. The next one starts over the same
	// records and edge, and records the LKG of the verified release first.
	w.cfg.Backend = standaloneBackend(t, ports, splitter, dir, starter)
	p := w.pipeline()
	if err := p.RecordVerifiedLKG(ctx, req); err != nil {
		t.Fatalf("RecordVerifiedLKG at startup: %v", err)
	}
	if lkg := w.lkg(); lkg.Digest != core.ReleaseDigest(firstBuild) || lkg.RunID != run {
		t.Fatalf("the LKG adopted at the restart is %+v, want the verified release", lkg)
	}
	if out, err := p.FinishVerified(ctx, req); err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified after the restart = %+v, %v; want closed", out, err)
	}
	if now := slotPID(t, dir, "green"); now != pid {
		t.Fatalf("the release's process %d was replaced by %d", pid, now)
	}
	if serving, err := w.cfg.Backend.ObservedRevision(ctx, testUnit); err != nil || serving.Digest != core.ReleaseDigest(firstBuild) || !serving.Healthy {
		t.Fatalf("after the restart %+v serves (%v), want the verified release", serving, err)
	}
}

package install

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// reply is what the fake network answers for one URL.
type reply struct {
	code   int
	body   string
	header http.Header
	err    error
}

// fakeNet answers HTTP requests from a table keyed by "host/path" and
// records them. A URL that is not in the table refuses the connection, like
// a port nothing listens on.
type fakeNet struct {
	mu     sync.Mutex
	routes map[string]reply
	seen   []*http.Request
}

func (n *fakeNet) RoundTrip(r *http.Request) (*http.Response, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.seen = append(n.seen, r)
	got, ok := n.routes[r.URL.Host+r.URL.Path]
	if !ok {
		return nil, errors.New("connection refused")
	}
	if got.err != nil {
		return nil, got.err
	}
	header := http.Header{}
	for k, v := range got.header {
		header[http.CanonicalHeaderKey(k)] = v
	}
	return &http.Response{
		StatusCode: got.code, Body: io.NopCloser(strings.NewReader(got.body)),
		Header: header, Request: r,
	}, nil
}

// requestTo returns the recorded requests for host/path.
func (n *fakeNet) requestsTo(hostPath string) []*http.Request {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []*http.Request
	for _, r := range n.seen {
		if r.URL.Host+r.URL.Path == hostPath {
			out = append(out, r)
		}
	}
	return out
}

const (
	healthyZones = `{"factory_id":"sisf-hello-app","status":"ok","zones":{"application":"ok","agent":"ok","control":"ok"}}`
	repoWritable = `{"full_name":"acme/hello-app","permissions":{"push":true,"pull":true}}`
)

// healthyNet is a factory that is up: every zone healthy, the repository
// writable by the token.
func healthyNet() *fakeNet {
	return &fakeNet{routes: map[string]reply{
		"127.0.0.1:7463/healthz":              {code: 200, body: healthyZones},
		"127.0.0.1:7460/healthz":              {code: 200, body: `{"status":"ok"}`},
		"127.0.0.1:7465/healthz":              {code: 200, body: `{"status":"ok"}`},
		"127.0.0.1:7467/repos/acme/hello-app": {code: 200, body: repoWritable},
		"127.0.0.1:7469/":                     {code: 401},
	}}
}

// withRoute returns n with one route replaced.
func (n *fakeNet) with(hostPath string, r reply) *fakeNet {
	n.routes[hostPath] = r
	return n
}

// without returns n with one route removed, as if nothing listened there.
func (n *fakeNet) without(hostPath string) *fakeNet {
	delete(n.routes, hostPath)
	return n
}

// supervisorWith answers the status of a supervisor that passed its
// self-check, with or without a last known good release.
func supervisorWith(lkg bool) func(context.Context, string) (supervisor.Status, error) {
	return func(context.Context, string) (supervisor.Status, error) {
		s := supervisor.Status{SelfCheck: supervisor.SelfCheckStatus{Passed: true, ManifestKeyID: "key"}}
		if lkg {
			s.LKG = supervisor.LKGStatus{Recorded: true, RecordedAt: fixedStart}
		}
		return s, nil
	}
}

// asThisInstall makes a fake supervisor name the factory and the root key of
// in, as the supervisor of this install does.
func asThisInstall(in *Installer, status func(context.Context, string) (supervisor.Status, error)) func(context.Context, string) (supervisor.Status, error) {
	return func(ctx context.Context, addr string) (supervisor.Status, error) {
		s, err := status(ctx, addr)
		m, merr := in.Layout.LoadManifest()
		if merr != nil {
			return s, merr
		}
		s.FactoryID, s.SelfCheck.ManifestKeyID = m.FactoryID, in.rootKeyID()
		return s, err
	}
}

// markRunning holds the run lock and writes the pid file of a foreground
// `sisf up`, and makes every pid alive, as for an install whose factory runs.
func markRunning(t *testing.T, in *Installer) {
	t.Helper()
	heldRunLocks.Store(in.Layout.Dir, holdRunLock(t, in, 4242, nil))
	in.Signal, in.ProcessStart = func(int, syscall.Signal) error { return nil }, everyPIDStarted
}

// heldRunLocks are the run locks markRunning holds, by install directory.
var heldRunLocks sync.Map

// markStopped ends the `sisf up` markRunning stood in for: its run lock is
// dropped, as the kernel drops it when the process ends, and no pid is alive.
func markStopped(in *Installer) {
	if lock, ok := heldRunLocks.LoadAndDelete(in.Layout.Dir); ok {
		_ = lock.(*os.File).Close()
	}
	signals := &fakeSignals{}
	in.Signal, in.ProcessStart = signals.send, signals.start
}

// builtStandalone returns an installer holding a Standalone install that was
// built, signed and is running; the fake network says what answers.
func builtStandalone(t *testing.T, net *fakeNet, mutate func(*Options)) *Installer {
	t.Helper()
	in, exec, _ := newTestInstaller(t)
	exec.answer = buildAnswers
	o := initOptions(in, manifest.ModeStandalone)
	if mutate != nil {
		mutate(&o)
	}
	if _, err := in.Init(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	m, err := in.Layout.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	b, err := in.release(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.installTrust(t.Context(), m, b); err != nil {
		t.Fatal(err)
	}
	if err := in.bootstrapState(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	in.HTTP = &http.Client{Transport: net, Timeout: time.Second}
	in.SupervisorStatus = asThisInstall(in, supervisorWith(true))
	net.mu.Lock()
	if r, ok := net.routes["127.0.0.1:7463/healthz"]; ok {
		r.body = strings.Replace(r.body, `"sisf-hello-app"`, `"`+string(m.FactoryID)+`"`, 1)
		net.routes["127.0.0.1:7463/healthz"] = r
	}
	net.mu.Unlock()
	markRunning(t, in)
	return in
}

// verdictOf returns the verdicts of every check with this name.
func verdictOf(r Report, name string) []Verdict {
	var out []Verdict
	for _, c := range r.Checks {
		if c.Name == name {
			out = append(out, c.Verdict)
		}
	}
	return out
}

// detailOf joins the details of the checks with this name.
func detailOf(r Report, name string) string {
	var out []string
	for _, c := range r.Checks {
		if c.Name == name {
			out = append(out, c.Detail)
		}
	}
	return strings.Join(out, "; ")
}

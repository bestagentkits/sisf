package harness

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// revisionHeader is where a harness application says which build it is.
const revisionHeader = "X-Harness-Revision"

// Deployment is an in-process deployment backend that releases by canary:
// each unit sits behind its own edge splitter, and each staged revision is
// a real HTTP server on an ephemeral port. What is observed running and
// what each revision served are read from the servers and the edge, never
// from what a call answered earlier.
type Deployment struct {
	t     testingT
	clock core.Clock

	mu      sync.Mutex
	units   map[string]*edge.Splitter
	servers map[core.ReleaseDigest]*httptest.Server
	faulty  map[core.ReleaseDigest]bool
	// faultNext marks the next revision staged of a unit as faulty.
	faultNext map[string]bool
	staged    []deploy.Revision
}

// testingT is what the deployment needs of a test.
type testingT interface {
	Helper()
	Fatal(args ...any)
	Cleanup(func())
	TempDir() string
}

var _ deploy.Backend = (*Deployment)(nil)

// newDeployment starts every unit at its last known good revision.
func newDeployment(t testingT, clock core.Clock, lkg map[string]core.ReleaseDigest) *Deployment {
	t.Helper()
	d := &Deployment{t: t, clock: clock, units: map[string]*edge.Splitter{}, servers: map[core.ReleaseDigest]*httptest.Server{},
		faulty: map[core.ReleaseDigest]bool{}, faultNext: map[string]bool{}}
	for unit, digest := range lkg {
		srv := d.serve(unit, digest)
		s, err := edge.NewSplitter(clock, slog.New(slog.DiscardHandler), edge.Target{Revision: string(digest), URL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		d.units[unit] = s
	}
	return d
}

// serve starts the server of one revision. Callers hold no lock.
func (d *Deployment) serve(unit string, digest core.ReleaseDigest) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		d.mu.Lock()
		broken := d.faulty[digest]
		d.mu.Unlock()
		w.Header().Set(revisionHeader, string(digest))
		if broken {
			http.Error(w, "injected fault", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, unit+" "+string(digest))
	}))
	d.t.Cleanup(srv.Close)
	d.mu.Lock()
	d.servers[digest] = srv
	d.mu.Unlock()
	return srv
}

// FaultNext makes the next revision staged of unit answer every request
// with 500, as a build that broke would.
func (d *Deployment) FaultNext(unit string) {
	d.mu.Lock()
	d.faultNext[unit] = true
	d.mu.Unlock()
}

// Staged returns every revision staged so far.
func (d *Deployment) Staged() []deploy.Revision {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]deploy.Revision(nil), d.staged...)
}

// Edge returns the edge of unit, which serves the unit's traffic.
func (d *Deployment) Edge(unit string) http.Handler { return d.splitter(unit) }

func (d *Deployment) splitter(unit string) *edge.Splitter {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.units[unit]
	if s == nil {
		d.t.Fatal("harness: no such unit " + unit)
	}
	return s
}

// Name names the backend.
func (*Deployment) Name() string { return "harness" }

// Capabilities declares everything a canary release needs.
func (*Deployment) Capabilities(context.Context) (core.CapabilitySet, error) {
	return core.CapabilitySet{
		core.CapDeployPreview: core.SupportSupported, core.CapDeployBlueGreen: core.SupportSupported,
		core.CapDeployCanary: core.SupportSupported, core.CapDeployMetrics: core.SupportSupported,
		core.CapDeployObservedRevision: core.SupportSupported, core.CapDeployRollback: core.SupportSupported,
	}, nil
}

// Stage starts the artifact's server without sending it traffic.
func (d *Deployment) Stage(_ context.Context, a deploy.Artifact) (deploy.Revision, error) {
	d.splitter(a.Unit)
	d.mu.Lock()
	_, running := d.servers[a.Digest]
	if d.faultNext[a.Unit] {
		d.faulty[a.Digest], d.faultNext[a.Unit] = true, false
	}
	rev := deploy.Revision{Unit: a.Unit, Digest: a.Digest, ID: a.Unit + "@" + string(a.Digest)}
	d.staged = append(d.staged, rev)
	d.mu.Unlock()
	if !running {
		d.serve(a.Unit, a.Digest)
	}
	return rev, nil
}

func (d *Deployment) url(digest core.ReleaseDigest) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	srv := d.servers[digest]
	if srv == nil {
		return "", core.NewError(core.CodeNotFound, "the revision is not staged")
	}
	return srv.URL, nil
}

// Preview returns the staged revision's own address.
func (d *Deployment) Preview(_ context.Context, rev deploy.Revision) (deploy.Preview, error) {
	u, err := d.url(rev.Digest)
	return deploy.Preview{Revision: rev, URL: u}, err
}

// Measure reads what the edge counted for rev over window and probes it.
func (d *Deployment) Measure(ctx context.Context, rev deploy.Revision, window time.Duration) (deploy.Metrics, error) {
	counts, err := d.splitter(rev.Unit).Counts(ctx, window)
	if err != nil {
		return deploy.Metrics{}, core.NewError(core.CodeReleaseHold, err.Error())
	}
	c := counts[string(rev.Digest)]
	_, healthy := d.probe(ctx, rev.Digest)
	now := d.clock.Now()
	return deploy.Metrics{Revision: rev, From: now.Add(-window), To: now, Requests: c.Samples, ServerErrors: c.ServerErrors, Healthy: healthy}, nil
}

// Promote sends percent of the unit's traffic to rev; 100 makes it stable.
func (d *Deployment) Promote(ctx context.Context, rev deploy.Revision, percent int) error {
	s := d.splitter(rev.Unit)
	target, err := d.url(rev.Digest)
	if err != nil {
		return err
	}
	next := edge.Target{Revision: string(rev.Digest), URL: target}
	if percent >= 100 {
		return s.SetSplit(ctx, edge.Split{Stable: next})
	}
	current, err := s.Split(ctx)
	if err != nil {
		return err
	}
	return s.SetSplit(ctx, edge.Split{Stable: current.Stable, Canary: &next, CanaryPercent: percent})
}

// ObservedRevision asks the unit's stable server which build it is.
func (d *Deployment) ObservedRevision(ctx context.Context, unit string) (deploy.Observed, error) {
	split, err := d.splitter(unit).Split(ctx)
	if err != nil {
		return deploy.Observed{}, err
	}
	digest, healthy := d.probe(ctx, core.ReleaseDigest(split.Stable.Revision))
	return deploy.Observed{Unit: unit, Digest: digest, Healthy: healthy, ObservedAt: d.clock.Now()}, nil
}

// Rollback makes to the stable revision again, with no canary.
func (d *Deployment) Rollback(ctx context.Context, to deploy.Revision) error {
	return d.Promote(ctx, to, 100)
}

// probe asks the server of digest which build it runs and whether it answers.
func (d *Deployment) probe(ctx context.Context, digest core.ReleaseDigest) (core.ReleaseDigest, bool) {
	u, err := d.url(digest)
	if err != nil {
		return "", false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"/healthz", nil)
	if err != nil {
		return "", false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	return core.ReleaseDigest(resp.Header.Get(revisionHeader)), resp.StatusCode == http.StatusOK
}

// Traffic sends n requests through the edge of unit and returns how many
// failed with a server error.
func (d *Deployment) Traffic(unit string, n int) (serverErrors int) {
	s := d.splitter(unit)
	for range n {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code >= 500 {
			serverErrors++
		}
	}
	return serverErrors
}

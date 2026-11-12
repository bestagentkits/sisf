package decision_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/decision"
	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/pkg/core"
	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
)

// manualClock moves only when the test moves it.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

// fakeCounters behaves like the edge: cumulative counters per endpoint.
type fakeCounters struct {
	mu     sync.Mutex
	totals map[string]decision.EndpointCounter
	err    error
}

func newCounters() *fakeCounters {
	return &fakeCounters{totals: map[string]decision.EndpointCounter{}}
}

func (f *fakeCounters) add(endpoint string, requests, errs uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.totals[endpoint]
	c.Endpoint = endpoint
	c.Requests += requests
	c.ServerErrors += errs
	f.totals[endpoint] = c
}

// set overwrites the totals, as a restarted edge would.
func (f *fakeCounters) set(endpoint string, requests, errs uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.totals[endpoint] = decision.EndpointCounter{Endpoint: endpoint, Requests: requests, ServerErrors: errs}
}

func (f *fakeCounters) EndpointCounters(context.Context) ([]decision.EndpointCounter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := make([]decision.EndpointCounter, 0, len(f.totals))
	for _, c := range f.totals {
		out = append(out, c)
	}
	return out, nil
}

// issueSink stands in for the component that opens issues from proposals.
type issueSink struct {
	mu     sync.Mutex
	issues []pkgdecision.Proposal
	err    error
}

func (s *issueSink) Propose(_ context.Context, p pkgdecision.Proposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.issues = append(s.issues, p)
	return nil
}

func (s *issueSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.issues)
}

func policyRefs() core.PolicyRefs {
	sha := func(c string) core.CommitSHA { return core.CommitSHA(strings.Repeat(c, 40)) }
	return core.PolicyRefs{PolicySHA: sha("a"), ReviewSHA: sha("b"), DecisionSHA: sha("c"), FeatureCatalogSHA: sha("d"),
		ModelRoute: core.ModelRoute{Provider: "none", Model: "deterministic"}}
}

type fixture struct {
	layer    *decision.Layer
	counters *fakeCounters
	sink     *issueSink
	store    *decision.MemoryProposals
	clock    *manualClock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, nil)
}

// newFixtureWith lets mutate change the configuration before the layer is built.
func newFixtureWith(t *testing.T, mutate func(*decision.Config)) *fixture {
	t.Helper()
	f := &fixture{
		counters: newCounters(), sink: &issueSink{}, store: decision.NewMemoryProposals(),
		clock: &manualClock{now: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)},
	}
	cfg := decision.Config{
		Source: f.counters, Sink: f.sink, Store: f.store, Clock: f.clock,
		Redactor: redact.New(redact.Config{}), Policy: policyRefs(), Rules: decision.DefaultRules(),
		Features: map[string]string{"POST /checkout": "checkout"},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	layer, err := decision.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.layer = layer
	f.run(t) // the first snapshot only sets the starting point
	return f
}

// window adds traffic to one endpoint, moves the clock one minute and runs
// the layer, returning the outcome for that endpoint.
func (f *fixture) window(t *testing.T, endpoint string, requests, errs uint64) decision.Outcome {
	t.Helper()
	f.counters.add(endpoint, requests, errs)
	return f.outcomeFor(t, endpoint)
}

func (f *fixture) outcomeFor(t *testing.T, endpoint string) decision.Outcome {
	t.Helper()
	for _, o := range f.run(t) {
		if o.Endpoint == endpoint {
			return o
		}
	}
	t.Fatalf("no outcome for %q", endpoint)
	return decision.Outcome{}
}

func (f *fixture) run(t *testing.T) []decision.Outcome {
	t.Helper()
	f.clock.After(time.Minute)
	out, err := f.layer.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// baseline gives an endpoint enough healthy windows to have a baseline.
func (f *fixture) baseline(t *testing.T, endpoint string) {
	t.Helper()
	for range decision.DefaultRules().MinBaselineWindows {
		if o := f.window(t, endpoint, 1000, 5); o.Verdict.CreatesWork() {
			t.Fatalf("a healthy window created work: %+v", o)
		}
	}
}

var errBoom = errors.New("boom")

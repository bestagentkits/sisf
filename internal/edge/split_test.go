package edge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

const (
	stableRev = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	canaryRev = "sha256:" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var splitStart = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

// revisionServer answers every request with status and claims to be
// another revision in its own header, to prove the edge overrides it.
func revisionServer(t *testing.T, status *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(edge.RevisionHeader, "claimed-by-upstream")
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(srv.Close)
	return srv
}

type splitEnv struct {
	clock                  *sqlstoretest.Clock
	splitter               *edge.Splitter
	handler                http.Handler
	stableCode, canaryCode *atomic.Int32
	stableURL, canaryURL   string
}

func newSplitEnv(t *testing.T) *splitEnv {
	t.Helper()
	e := &splitEnv{clock: sqlstoretest.NewClock(splitStart), stableCode: &atomic.Int32{}, canaryCode: &atomic.Int32{}}
	e.stableCode.Store(200)
	e.canaryCode.Store(200)
	e.stableURL, e.canaryURL = revisionServer(t, e.stableCode).URL, revisionServer(t, e.canaryCode).URL
	var err error
	if e.splitter, err = edge.NewSplitter(e.clock, nil, edge.Target{Revision: stableRev, URL: e.stableURL}); err != nil {
		t.Fatal(err)
	}
	if e.handler, err = edge.New(edge.Config{Split: e.splitter}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *splitEnv) setCanary(t *testing.T, percent int) {
	t.Helper()
	err := e.splitter.SetSplit(context.Background(), edge.Split{
		Stable: edge.Target{Revision: stableRev, URL: e.stableURL},
		Canary: &edge.Target{Revision: canaryRev, URL: e.canaryURL}, CanaryPercent: percent,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// serve sends n requests and returns how many carried each revision header.
func (e *splitEnv) serve(n int, target string) map[string]int {
	seen := map[string]int{}
	for range n {
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		seen[rec.Header().Get(edge.RevisionHeader)]++
	}
	return seen
}

func TestTheSplitSendsExactlyTheConfiguredShareToTheCanary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ percent, requests, wantCanary int }{
		{5, 200, 10}, {25, 100, 25}, {50, 100, 50}, {1, 300, 3}, {99, 100, 99},
	} {
		e := newSplitEnv(t)
		e.setCanary(t, tc.percent)
		seen := e.serve(tc.requests, "/")
		if seen[canaryRev] != tc.wantCanary || seen[stableRev] != tc.requests-tc.wantCanary {
			t.Errorf("%d%% of %d requests: canary %d stable %d, want canary %d", tc.percent, tc.requests, seen[canaryRev], seen[stableRev], tc.wantCanary)
		}
	}
}

func TestWithoutACanaryEverythingGoesToTheStable(t *testing.T) {
	t.Parallel()
	e := newSplitEnv(t)
	if seen := e.serve(50, "/"); seen[stableRev] != 50 {
		t.Fatalf("seen = %v, want all 50 on the stable", seen)
	}
}

func TestTheRevisionHeaderIsTheEdgesAndNotTheUpstreams(t *testing.T) {
	t.Parallel()
	e := newSplitEnv(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(edge.RevisionHeader, canaryRev) // a client cannot pick a revision
	e.handler.ServeHTTP(rec, req)
	if got := rec.Result().Header.Values(edge.RevisionHeader); len(got) != 1 || got[0] != stableRev {
		t.Fatalf("revision header = %v, want exactly the stable revision", got)
	}
}

func TestCountersAreKeptPerRevisionWithFiveHundredsAndSamples(t *testing.T) {
	t.Parallel()
	e := newSplitEnv(t)
	e.setCanary(t, 50)
	e.canaryCode.Store(500)
	e.serve(100, "/")

	counts, err := e.splitter.Counts(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := counts[stableRev], (edge.Counts{Requests: 50, Samples: 50}); got != want {
		t.Errorf("stable counts = %+v, want %+v", got, want)
	}
	if got, want := counts[canaryRev], (edge.Counts{Requests: 50, Samples: 50, ServerErrors: 50}); got != want {
		t.Errorf("canary counts = %+v, want %+v", got, want)
	}
}

func TestAnUnreachableUpstreamCountsAsAServerErrorOfItsRevision(t *testing.T) {
	t.Parallel()
	e := newSplitEnv(t)
	e.setCanary(t, 50)
	err := e.splitter.SetSplit(context.Background(), edge.Split{
		Stable: edge.Target{Revision: stableRev, URL: e.stableURL},
		Canary: &edge.Target{Revision: canaryRev, URL: "http://127.0.0.1:1"}, CanaryPercent: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.serve(10, "/")
	counts, _ := e.splitter.Counts(context.Background(), 0)
	if counts[canaryRev].ServerErrors != 5 || counts[stableRev].ServerErrors != 0 {
		t.Fatalf("counts = %+v, want the five failed canary requests as server errors", counts)
	}
}

func TestWindowedCountsForgetWhatIsOlderThanTheWindow(t *testing.T) {
	t.Parallel()
	e := newSplitEnv(t)
	e.serve(10, "/")
	e.clock.Advance(5 * time.Minute)
	e.serve(3, "/")

	recent, err := e.splitter.Counts(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got := recent[stableRev].Requests; got != 3 {
		t.Errorf("requests in the last minute = %d, want 3", got)
	}
	longer, _ := e.splitter.Counts(context.Background(), 10*time.Minute)
	if got := longer[stableRev].Requests; got != 13 {
		t.Errorf("requests in the last ten minutes = %d, want 13", got)
	}
	for _, bad := range []time.Duration{-time.Second, 2 * time.Hour} {
		if _, err := e.splitter.Counts(context.Background(), bad); err == nil {
			t.Errorf("window %v was accepted", bad)
		}
	}
}

func TestRoutesAreNeitherSplitNorCounted(t *testing.T) {
	t.Parallel()
	e := newSplitEnv(t)
	control := newUpstream(t, "control")
	h, err := edge.New(edge.Config{Split: e.splitter, Routes: []edge.Route{mustRoute(t, "/webhooks/github="+control.URL)}})
	if err != nil {
		t.Fatal(err)
	}
	e.setCanary(t, 50)
	if _, body := get(t, h, "/webhooks/github"); body != "control" {
		t.Fatalf("route body = %q, want control", body)
	}
	if counts, _ := e.splitter.Counts(context.Background(), 0); len(counts) != 0 {
		t.Fatalf("a routed request was counted: %+v", counts)
	}
}

func TestARefusedSplitChangesNothing(t *testing.T) {
	t.Parallel()
	e := newSplitEnv(t)
	stable := edge.Target{Revision: stableRev, URL: e.stableURL}
	canary := &edge.Target{Revision: canaryRev, URL: e.canaryURL}
	for name, split := range map[string]edge.Split{
		"percent without canary": {Stable: stable, CanaryPercent: 10},
		"canary without percent": {Stable: stable, Canary: canary},
		"percent of one hundred": {Stable: stable, Canary: canary, CanaryPercent: 100},
		"same revision":          {Stable: stable, Canary: &edge.Target{Revision: stableRev, URL: e.canaryURL}, CanaryPercent: 5},
		"header injection":       {Stable: edge.Target{Revision: "a\r\nX-Evil: 1", URL: e.stableURL}},
		"empty revision":         {Stable: edge.Target{URL: e.stableURL}},
		"upstream with a path":   {Stable: edge.Target{Revision: stableRev, URL: e.stableURL + "/inner"}},
	} {
		if err := e.splitter.SetSplit(context.Background(), split); err == nil {
			t.Errorf("%s: split was accepted", name)
		}
	}
	cur, _ := e.splitter.Split(context.Background())
	if cur.Canary != nil || cur.Stable.Revision != stableRev {
		t.Fatalf("split after refusals = %+v, want the original", cur)
	}
	if !strings.Contains(e.serveOne(), stableRev) {
		t.Fatal("traffic no longer reaches the stable")
	}
}

func (e *splitEnv) serveOne() string {
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Header().Get(edge.RevisionHeader)
}

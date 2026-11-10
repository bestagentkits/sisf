package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/decision"
	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// The decision layer reads what each revision served since the edge
// started, through the edge's admin route.
func TestEdgeCountersAreTheCumulativeCountsOfEachRevision(t *testing.T) {
	t.Parallel()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(app.Close)
	clock := sqlstoretest.NewClock(time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC))
	splitter, err := edge.NewSplitter(clock, nil, edge.Target{Revision: "v1", URL: app.URL})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(splitter)
	t.Cleanup(front.Close)
	admin, err := edge.NewAdminHandler(splitter, edgeToken)
	if err != nil {
		t.Fatal(err)
	}
	adminSrv := httptest.NewServer(admin)
	t.Cleanup(adminSrv.Close)
	for _, path := range []string{"/", "/", "/fail"} {
		resp, err := http.Get(front.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	client, err := edge.NewAdminClient(adminSrv.URL, edgeToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Hour)
	got, err := edgeCounters{edge: client}.EndpointCounters(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []decision.EndpointCounter{{Endpoint: "v1", Requests: 3, ServerErrors: 1}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("counters = %+v, want %+v", got, want)
	}
}

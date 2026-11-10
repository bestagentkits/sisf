package edge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
)

const countsToken = "counts-token-for-tests"

func countsServer(t *testing.T, e *splitEnv) *httptest.Server {
	t.Helper()
	h, err := edge.NewAdminHandler(e.splitter, adminToken, edge.WithCountsToken(countsToken))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestTheCountsTokenReadsTheCountersAndNothingElse(t *testing.T) {
	t.Parallel()
	e := newSplitEnv(t)
	srv := countsServer(t, e)
	e.serve(10, "/")
	reader, err := edge.NewCountsClient(srv.URL, countsToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counts, err := reader.Counts(context.Background(), 10*time.Minute); err != nil || counts[stableRev].Requests != 10 {
		t.Fatalf("Counts = %+v, %v", counts, err)
	}
	valid := `{"stable":{"revision":"r9","url":"` + e.stableURL + `"}}`
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPut, "/split", valid},
		{http.MethodGet, "/split", ""},
		{http.MethodPost, "/counters", ""},
		{http.MethodDelete, "/counters", ""},
		{http.MethodGet, "/counters/", ""},
		{http.MethodGet, "/other", ""},
	} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, strings.NewReader(c.body))
		req.Header.Set("Authorization", "Bearer "+countsToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with the counters token = %d, want 403", c.method, c.path, resp.StatusCode)
		}
	}
	// An admin client handed the counters token by mistake still cannot move traffic.
	misused, _ := edge.NewAdminClient(srv.URL, countsToken, nil)
	if err := misused.SetSplit(context.Background(), edge.Split{Stable: edge.Target{Revision: "r9", URL: e.stableURL}}); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("SetSplit with the counters token: %v, want PERMISSION_DENIED", err)
	}
	if cur, _ := e.splitter.Split(context.Background()); cur.Stable.Revision != stableRev {
		t.Fatal("the counters token changed the split")
	}
	admin, _ := edge.NewAdminClient(srv.URL, adminToken, nil)
	if _, err := admin.Split(context.Background()); err != nil {
		t.Fatalf("the admin token stops working next to a counters token: %v", err)
	}
}

func TestTheCountsTokenMustDifferFromTheAdminToken(t *testing.T) {
	t.Parallel()
	e := newSplitEnv(t)
	if _, err := edge.NewAdminHandler(e.splitter, adminToken, edge.WithCountsToken(adminToken)); err == nil {
		t.Fatal("a counters token equal to the admin token was accepted")
	}
	if _, err := edge.NewAdminHandler(e.splitter, "", edge.WithCountsToken(countsToken)); err == nil {
		t.Fatal("a counters token without an admin token was accepted")
	}
}

// The counts client is the type a reader of the counters holds; it must
// offer no way to read or change the split.
func TestTheCountsClientHasNoWayToTouchTheSplit(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[*edge.CountsClient]()
	for _, name := range []string{"SetSplit", "Split"} {
		if _, ok := typ.MethodByName(name); ok {
			t.Errorf("*edge.CountsClient has %s", name)
		}
	}
	if _, ok := any(&edge.CountsClient{}).(edge.Controller); ok {
		t.Fatal("*edge.CountsClient satisfies edge.Controller")
	}
	if _, err := edge.NewCountsClient("http://edge:7470", "", nil); err == nil {
		t.Fatal("a counts client without a token was built")
	}
}

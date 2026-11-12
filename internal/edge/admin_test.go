package edge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
)

const adminToken = "admin-token-for-tests"

func adminServer(t *testing.T, e *splitEnv) *httptest.Server {
	t.Helper()
	h, err := edge.NewAdminHandler(e.splitter, adminToken)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestTheAdminClientMovesTrafficAndReadsTheCounters(t *testing.T) {
	t.Parallel()
	e := newSplitEnv(t)
	client, err := edge.NewAdminClient(adminServer(t, e).URL, adminToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	split := edge.Split{
		Stable: edge.Target{Revision: stableRev, URL: e.stableURL},
		Canary: &edge.Target{Revision: canaryRev, URL: e.canaryURL}, CanaryPercent: 20,
	}
	if err := client.SetSplit(ctx, split); err != nil {
		t.Fatal(err)
	}
	got, err := client.Split(ctx)
	if err != nil || got.Canary == nil || got.CanaryPercent != 20 || got.Canary.Revision != canaryRev {
		t.Fatalf("Split = %+v, %v, want the canary at 20%%", got, err)
	}
	e.serve(100, "/")
	counts, err := client.Counts(ctx, 10*time.Minute)
	if err != nil || counts[canaryRev].Requests != 20 || counts[stableRev].Requests != 80 {
		t.Fatalf("Counts = %+v, %v, want 20 canary and 80 stable", counts, err)
	}
	totals, err := client.Counts(ctx, 0)
	if err != nil || totals[canaryRev].Samples != 20 {
		t.Fatalf("totals = %+v, %v", totals, err)
	}
}

func TestTheAdminSurfaceRefusesAMissingOrWrongToken(t *testing.T) {
	t.Parallel()
	e := newSplitEnv(t)
	srv := adminServer(t, e)
	for name, auth := range map[string]string{"none": "", "wrong": "Bearer nope", "scheme": "Basic " + adminToken, "bare": adminToken} {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/split", strings.NewReader(`{}`))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := testClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, resp.StatusCode)
		}
	}
	bad, _ := edge.NewAdminClient(srv.URL, "wrong", nil)
	if err := bad.SetSplit(context.Background(), edge.Split{}); !core.HasCode(err, core.CodeUnauthenticated) {
		t.Fatalf("client with a wrong token: %v, want UNAUTHENTICATED", err)
	}
}

func TestTheAdminSurfaceRefusesBadBodies(t *testing.T) {
	t.Parallel()
	e := newSplitEnv(t)
	srv := adminServer(t, e)
	valid := `{"stable":{"revision":"r1","url":"` + e.stableURL + `"}`
	for name, body := range map[string]string{
		"unknown field": valid + `,"admin":true}`,
		"two objects":   valid + `}{}`,
		"not json":      `stable`,
		"bad split":     valid + `,"canary_percent":7}`,
	} {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/split", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+adminToken)
		resp, err := testClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}
	if cur, _ := e.splitter.Split(context.Background()); cur.Stable.Revision != stableRev {
		t.Fatal("a refused body changed the split")
	}
}

func TestAnAdminHandlerNeedsAToken(t *testing.T) {
	t.Parallel()
	e := newSplitEnv(t)
	if _, err := edge.NewAdminHandler(e.splitter, ""); err == nil {
		t.Fatal("an admin handler without a token was built")
	}
	if _, err := edge.NewAdminClient("http://edge:7470/x", "t", nil); err == nil {
		t.Fatal("an admin address with a path was accepted")
	}
}

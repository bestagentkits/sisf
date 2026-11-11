package wiring_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/state"
)

// keptReports counts what the control zone keeps of bug reports under
// prefixes: r/ for those waiting to be published, p/ for published ones.
func keptReports(t *testing.T, f *harness.Factory, prefixes ...string) int {
	t.Helper()
	var n int
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		for _, prefix := range prefixes {
			entries, err := tx.ListKV(ctx, control.ReportsNamespace, prefix, 0)
			if err != nil {
				return err
			}
			n += len(entries)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// sendReport posts a bug report to the control zone's public report route.
func sendReport(t *testing.T, f *harness.Factory, title, body string) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"title": title, "body": body})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, report.Path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.Control.Server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("the report: %d %s", rec.Code, rec.Body)
	}
}

// What is kept of a bug report is removed by the control pass once its
// retention has passed, even when no new report arrives to prune it.
func TestTheControlPassPrunesLapsedReports(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	sendReport(t, f, "Checkout fails", "The page fails.")
	f.Pass()
	if n := keptReports(t, f, "r/", "p/"); n != 1 {
		t.Fatalf("%d reports are kept within their retention, want 1", n)
	}
	f.Clock.Advance(redact.DefaultRetention()[redact.KindReport] + 24*time.Hour)
	f.Pass()
	if n := keptReports(t, f, "r/", "p/"); n != 0 {
		t.Fatalf("%d lapsed reports outlived the control pass", n)
	}
}

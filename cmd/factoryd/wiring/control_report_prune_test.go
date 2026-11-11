package wiring_test

import (
	"context"
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

// storedReports counts the bug reports the control zone keeps.
func storedReports(t *testing.T, f *harness.Factory) int {
	t.Helper()
	var n int
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entries, err := tx.ListKV(ctx, control.ReportsNamespace, "r/", 0)
		n = len(entries)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A stored bug report is removed by the control pass once its retention
// has passed, even when no new report arrives to prune it.
func TestTheControlPassPrunesLapsedReports(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	req := httptest.NewRequest(http.MethodPost, report.Path, strings.NewReader(`{"title":"Checkout fails","body":"The page fails."}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.Control.Server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("the report: %d %s", rec.Code, rec.Body)
	}
	f.Pass()
	if n := storedReports(t, f); n != 1 {
		t.Fatalf("%d reports are stored within their retention, want 1", n)
	}
	f.Clock.Advance(redact.DefaultRetention()[redact.KindReport] + 24*time.Hour)
	f.Pass()
	if n := storedReports(t, f); n != 0 {
		t.Fatalf("%d lapsed reports outlived the control pass", n)
	}
}

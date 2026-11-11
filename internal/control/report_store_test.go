package control_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// postReport posts the report titled title from its own client address and
// returns the status and the answer's state.
func postReport(t *testing.T, e *env, title string, client int) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"title":%q,"body":"The page fails."}`, title)
	req := httptest.NewRequest(http.MethodPost, report.Path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", client%250+1)
	rec := e.do(req)
	var answer struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the report route answered %d with no JSON: %q", rec.Code, rec.Body.String())
	}
	return rec.Code, answer.Status
}

// A route given no store of its own keeps reports in the control zone's
// operational store, so a restart of control loses none: the same report
// sent again afterwards is counted as a duplicate of the stored one.
func TestReportsAreKeptInTheControlStoreAcrossARestart(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e := newEnv(t, db)
		e.deps.Report = &control.ReportRoute{Redactor: redact.New(redact.Config{})}
		e.restart(t)

		if code, state := postReport(t, e, "Checkout fails", 1); code != http.StatusAccepted || state != "accepted" {
			t.Fatalf("a new report: %d %s", code, state)
		}
		e.restart(t)
		if code, state := postReport(t, e, "Checkout fails", 2); code != http.StatusOK || state != "duplicate" {
			t.Fatalf("the same report after a restart: %d %s, want it counted against the stored one", code, state)
		}
		if code, state := postReport(t, e, "Search is slow", 3); code != http.StatusAccepted || state != "accepted" {
			t.Fatalf("another report after a restart: %d %s", code, state)
		}
	})
}

// The stored reports are bounded: a full store makes room for a new report
// by removing one of the oldest day's, still counts a duplicate, and a
// report past its retention is removed, which makes room again.
func TestStoredReportsAreBoundedAndExpireAfterTheirRetention(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e := newEnv(t, db)
		const capacity = 3
		retention := 48 * time.Hour
		e.deps.Report = &control.ReportRoute{Redactor: redact.New(redact.Config{}), MaxStored: capacity, Retention: retention}
		e.restart(t)

		for n := range capacity {
			if code, _ := postReport(t, e, fmt.Sprintf("Old report %d", n), n); code != http.StatusAccepted {
				t.Fatalf("report %d: %d", n, code)
			}
		}
		if code, state := postReport(t, e, "One too many", 10); code != http.StatusAccepted || state != "accepted" {
			t.Fatalf("a report past the capacity: %d %s, want it stored in place of an older one", code, state)
		}
		if code, state := postReport(t, e, "One too many", 11); code != http.StatusOK || state != "duplicate" {
			t.Fatalf("a duplicate in a full store: %d %s", code, state)
		}
		if n := storedReportCount(t, e); n != capacity {
			t.Fatalf("%d reports are stored, want the capacity of %d", n, capacity)
		}

		// A day later, new reports push out the previous day's first.
		db.Clock.Advance(24 * time.Hour)
		for n := range capacity {
			if code, _ := postReport(t, e, fmt.Sprintf("Next day %d", n), 20+n); code != http.StatusAccepted {
				t.Fatalf("next day's report %d in a full store: %d, want it stored", n, code)
			}
		}
		for n := range capacity {
			if code, state := postReport(t, e, fmt.Sprintf("Next day %d", n), 30+n); code != http.StatusOK || state != "duplicate" {
				t.Fatalf("next day's report %d again: %d %s, want it kept over the previous day's", n, code, state)
			}
		}
		if code, state := postReport(t, e, "One too many", 40); code != http.StatusAccepted || state != "accepted" {
			t.Fatalf("a report of the previous day sent again: %d %s, want it removed to make room and stored afresh", code, state)
		}

		db.Clock.Advance(retention + 24*time.Hour)
		if code, state := postReport(t, e, "Next day 1", 50); code != http.StatusAccepted || state != "accepted" {
			t.Fatalf("a report whose stored copy lapsed: %d %s, want it stored afresh", code, state)
		}
	})
}

// Lapsed reports are removed by the control zone's periodic pass too, so
// they do not outlive their retention when no new report arrives.
func TestLapsedReportsArePrunedWithoutANewReport(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e := newEnv(t, db)
		retention := 48 * time.Hour
		e.deps.Report = &control.ReportRoute{Redactor: redact.New(redact.Config{}), Retention: retention}
		e.restart(t)
		const sent = 40
		for n := range sent {
			if code, _ := postReport(t, e, fmt.Sprintf("Report %d", n), n); code != http.StatusAccepted {
				t.Fatalf("report %d: %d", n, code)
			}
		}
		ctx := context.Background()
		if err := e.server.PruneReports(ctx); err != nil {
			t.Fatal(err)
		}
		if n := storedReportCount(t, e); n != sent {
			t.Fatalf("a prune within the retention left %d of %d reports", n, sent)
		}
		db.Clock.Advance(retention + 24*time.Hour)
		if err := e.server.PruneReports(ctx); err != nil {
			t.Fatal(err)
		}
		if n := storedReportCount(t, e); n != 0 {
			t.Fatalf("%d lapsed reports outlived the periodic prune", n)
		}
		if code, state := postReport(t, e, "Report 0", 99); code != http.StatusAccepted || state != "accepted" {
			t.Fatalf("a report whose stored copy was pruned: %d %s, want it stored afresh", code, state)
		}
	})
}

// A control server built without the report route has nothing to prune.
func TestPruningWithoutAReportRouteDoesNothing(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e := newEnv(t, db)
		e.deps.Report = nil
		e.restart(t)
		if err := e.server.PruneReports(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

// storedReportCount counts the reports in the store and checks the stored
// counter agrees.
func storedReportCount(t *testing.T, e *env) int {
	t.Helper()
	var n int
	var counter string
	err := e.store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entries, err := tx.ListKV(ctx, control.ReportsNamespace, "r/", 0)
		if err != nil {
			return err
		}
		n = len(entries)
		entry, err := tx.GetKV(ctx, control.ReportsNamespace, "count")
		if err == nil {
			counter = string(entry.Value)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(n) != counter && !(n == 0 && counter == "") {
		t.Errorf("the stored counter says %q, the store holds %d", counter, n)
	}
	return n
}

// Reports come from the public internet and are kept for the control zone
// alone: the agent and application zones cannot read their namespace.
func TestOnlyTheControlZoneReachesTheStoredReports(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		for _, zone := range []core.Zone{core.ZoneAgent, core.ZoneApplication} {
			store := db.Open(t, zone)
			err := store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
				_, err := tx.ListKV(ctx, control.ReportsNamespace, "", 1)
				return err
			})
			if !core.HasCode(err, core.CodePermissionDenied) {
				t.Errorf("the %s zone listed the stored reports: %v", zone, err)
			}
		}
	})
}

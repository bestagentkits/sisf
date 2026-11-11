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

// The stored reports are bounded: a full store refuses a new report and
// still counts a duplicate, and a report past its retention is removed, which
// makes room again.
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
		if code, _ := postReport(t, e, "One too many", 10); code != http.StatusServiceUnavailable {
			t.Fatalf("a report past the capacity: %d, want it refused", code)
		}
		if code, state := postReport(t, e, "Old report 0", 11); code != http.StatusOK || state != "duplicate" {
			t.Fatalf("a duplicate in a full store: %d %s", code, state)
		}

		db.Clock.Advance(retention + 24*time.Hour)
		if code, state := postReport(t, e, "Old report 1", 12); code != http.StatusAccepted || state != "accepted" {
			t.Fatalf("a report whose stored copy lapsed: %d %s, want it stored afresh", code, state)
		}
		for n := range capacity - 1 {
			if code, _ := postReport(t, e, fmt.Sprintf("New report %d", n), 20+n); code != http.StatusAccepted {
				t.Fatalf("new report %d after the old ones lapsed: %d", n, code)
			}
		}
		if code, _ := postReport(t, e, "Over again", 30); code != http.StatusServiceUnavailable {
			t.Fatalf("a report past the capacity after pruning: %d, want it refused", code)
		}
	})
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

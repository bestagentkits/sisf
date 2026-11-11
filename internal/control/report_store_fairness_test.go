package control_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// smallReportEnv is reportEnv with a store of capacity reports.
func smallReportEnv(t *testing.T, db *sqlstoretest.Database, capacity int) (*env, *reportGitHub, control.ReportPublisher) {
	t.Helper()
	e, gh, p := reportEnv(t, db)
	e.deps.Report.MaxStored = capacity
	e.restart(t)
	return e, gh, p
}

func readPublication(t *testing.T, e *env, now time.Time) control.ReportPublication {
	t.Helper()
	st, err := control.ReadReportPublication(context.Background(), e.store, now)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// Twelve addresses flooding a full store push out each other's newest
// reports, never a person's: a person's report from a new address is
// accepted, and it is published in the order it was received, within the
// day's budget. What was pushed out is counted in the status.
func TestAFloodFillingTheStoreDoesNotStopAPersonsReport(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		const capacity = 30
		e, gh, p := smallReportEnv(t, db, capacity)
		accepted := 0
		for client := 1; client <= 12; client++ {
			for n := range 5 {
				switch code, _ := sendReportText(t, e, fmt.Sprintf("Flood %d from %d", n, client), "Body.", client); code {
				case http.StatusAccepted:
					accepted++
				case http.StatusServiceUnavailable:
				default:
					t.Fatalf("flood report %d from %d: %d", n, client, code)
				}
				db.Clock.Advance(100 * time.Millisecond)
			}
		}
		if accepted <= capacity {
			t.Fatalf("%d flood reports accepted into a store of %d, want the busiest addresses' newest pushed out for later ones", accepted, capacity)
		}
		db.Clock.Advance(time.Hour) // past the hour the flood spent the overall limit in
		if code, _ := sendReportText(t, e, "Checkout fails", "A person's report.", 200); code != http.StatusAccepted {
			t.Fatalf("a person's report into a store the flood filled: %d, want it accepted", code)
		}
		st := readPublication(t, e, db.Clock.Now())
		if st.Waiting != capacity || st.Dropped != accepted+1-capacity {
			t.Fatalf("the status after the flood: %+v, want %d waiting and %d dropped", st, capacity, accepted+1-capacity)
		}
		for range capacity {
			publish(t, p)
			db.Clock.Advance(time.Minute)
		}
		issues := gh.created()
		if len(issues) != capacity || issues[len(issues)-1].Title != "Report: Checkout fails" {
			titles := make([]string, len(issues))
			for i, issue := range issues {
				titles[i] = issue.Title
			}
			t.Fatalf("%d issues opened, want %d with the person's report last as it came last:\n%s", len(issues), capacity, strings.Join(titles, "\n"))
		}
	})
}

// With every waiting report from a different address, none holds more than
// its share: a new report is refused, with a time to retry, rather than
// pushing out anyone's.
func TestAStoreOfOneReportPerAddressRefusesANewcomer(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, _, _ := smallReportEnv(t, db, 5)
		for client := 1; client <= 5; client++ {
			if code, _ := sendReportText(t, e, fmt.Sprintf("Report from %d", client), "Body.", client); code != http.StatusAccepted {
				t.Fatalf("report from %d: %d", client, code)
			}
		}
		req := httptest.NewRequest(http.MethodPost, report.Path, strings.NewReader(`{"title":"One too many","body":"Body."}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "198.51.100.7:4000"
		if rec := e.do(req); rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("a newcomer into a store of one report per address: %d, Retry-After %q; want it refused with a time to retry",
				rec.Code, rec.Header().Get("Retry-After"))
		}
		if st := readPublication(t, e, db.Clock.Now()); st.Waiting != 5 || st.Dropped != 0 {
			t.Fatalf("the status: %+v, want 5 waiting and none dropped", st)
		}
	})
}

// One address at its limit hour after hour fills the store, but cannot
// keep anyone else out: another address's report pushes out its newest,
// while the address itself is refused once it holds the whole store.
func TestOneAddressCannotHoldTheStoreAgainstOthers(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, _, _ := smallReportEnv(t, db, 10)
		for hour := range 2 {
			for n := range 5 {
				if code, _ := sendReportText(t, e, fmt.Sprintf("Hostage %d-%d", hour, n), "Body.", 1); code != http.StatusAccepted {
					t.Fatalf("report %d-%d: %d", hour, n, code)
				}
			}
			db.Clock.Advance(time.Hour)
		}
		if code, _ := sendReportText(t, e, "Hostage more", "Body.", 1); code != http.StatusServiceUnavailable {
			t.Fatalf("the address holding the whole store sends more: %d, want it refused", code)
		}
		for client := 2; client <= 6; client++ {
			if code, _ := sendReportText(t, e, fmt.Sprintf("Another %d", client), "Body.", client); code != http.StatusAccepted {
				t.Fatalf("another address's report into a store one address holds: %d, want it accepted", code)
			}
		}
		if st := readPublication(t, e, db.Clock.Now()); st.Waiting != 10 || st.Dropped != 5 {
			t.Fatalf("the status: %+v, want 10 waiting and 5 dropped", st)
		}
	})
}

// A report whose publication is under way is never pushed out: the
// busiest address's newest report that is not being published goes
// instead.
func TestAReportBeingPublishedIsNeverPushedOut(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := smallReportEnv(t, db, 2)
		sendReportText(t, e, "Older", "Body.", 1)
		db.Clock.Advance(time.Second)
		sendReportText(t, e, "Newer", "Body.", 1)
		// GitHub refuses the older report once, which leaves it waiting,
		// and then breaks while the newer one is asked for, which leaves
		// that one's publication under way.
		gh.fail = []error{rejected(422), core.NewError(core.CodeUnavailable, "GitHub is down")}
		if _, err := p.Publish(context.Background()); err == nil {
			t.Fatal("the broken publication reported no error")
		}
		if code, _ := sendReportText(t, e, "Newcomer", "Body.", 2); code != http.StatusAccepted {
			t.Fatalf("a newcomer: %d, want the older, idle report pushed out", code)
		}
		for range 3 {
			publish(t, p)
		}
		var titles []string
		for _, issue := range gh.created() {
			titles = append(titles, issue.Title)
		}
		if !slices.Contains(titles, "Report: Newer") || slices.Contains(titles, "Report: Older") {
			t.Fatalf("issues %v, want the report being published kept and the idle one pushed out", titles)
		}
	})
}

// The store keeps the client of a waiting report only as a keyed hash,
// never the address, and keeps nothing of it once the report is
// published.
func TestTheStoreNeverKeepsAReportersAddress(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, _, p := reportEnv(t, db)
		const address = "198.51.100.8"
		sendReportText(t, e, "Search is slow", "Body.", 7)
		search := func() error {
			return e.store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
				entries, err := tx.ListKV(ctx, control.ReportsNamespace, "", 0)
				if err != nil {
					return err
				}
				for _, entry := range entries {
					if strings.Contains(entry.Key, address) || strings.Contains(string(entry.Value), address) {
						return errors.New("the store holds the address at " + entry.Key)
					}
				}
				return nil
			})
		}
		if err := search(); err != nil {
			t.Fatal(err)
		}
		publish(t, p)
		if err := search(); err != nil {
			t.Fatal(err)
		}
	})
}

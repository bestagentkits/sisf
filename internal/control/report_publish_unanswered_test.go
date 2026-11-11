package control_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// publishCharges reads how many attempts are charged against the daily
// budget of report issues, and how many waiting reports have an attempt
// whose answer was not recorded.
func publishCharges(t *testing.T, e *env) (charged, live int) {
	t.Helper()
	err := e.store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, control.ReportsNamespace, "publish-budget")
		switch {
		case err == nil:
			var times []time.Time
			if err := json.Unmarshal(entry.Value, &times); err != nil {
				return err
			}
			charged = len(times)
		case !core.HasCode(err, core.CodeNotFound):
			return err
		}
		waiting, err := tx.ListKV(ctx, control.ReportsNamespace, "r/", 0)
		for _, w := range waiting {
			var stored struct {
				Publish struct {
					AttemptedAt time.Time `json:"attempted_at"`
				} `json:"publish"`
			}
			if json.Unmarshal(w.Value, &stored) == nil && !stored.Publish.AttemptedAt.IsZero() {
				live++
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return charged, live
}

// GitHub listing issues but answering every new one with an outage opens
// nothing: each report's unanswered attempt is looked for, found missing,
// and its charge taken back as the next is made, so the budget holds at
// most one charge per report still waiting for an answer. After three
// unanswered attempts a report is given up, counted as failed, so the
// reports behind it are tried in turn, and publishing is never paused for
// the day.
func TestReportsWhoseIssuesGitHubNeverAnswersAreGivenUpWithoutSpendingTheBudget(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		const reports = 5
		for n := range reports {
			if code, _ := sendReportText(t, e, fmt.Sprint("Report ", n), "Body.", n); code != http.StatusAccepted {
				t.Fatalf("report %d: %d", n, code)
			}
		}
		for range 60 {
			gh.fail = append(gh.fail, rejected(http.StatusBadGateway))
		}
		for pass := range 60 {
			res, _ := p.Publish(context.Background())
			if !res.PausedUntil.IsZero() {
				t.Fatalf("pass %d: publishing paused until %s by attempts that opened nothing", pass, res.PausedUntil)
			}
			if charged, live := publishCharges(t, e); charged > live {
				t.Fatalf("pass %d: %d attempts charged with %d reports waiting for an answer", pass, charged, live)
			}
			db.Clock.Advance(time.Minute)
		}
		if gh.creates != 3*reports {
			t.Fatalf("%d issues asked for, want 3 for each of the %d reports", gh.creates, reports)
		}
		if waiting, failed := countKeys(t, e, "r/"), countKeys(t, e, "f/"); waiting != 0 || failed != reports {
			t.Fatalf("%d reports waiting and %d given up, want every report given up", waiting, failed)
		}
		st := readPublication(t, e, db.Clock.Now())
		if st.Failed != reports || st.Waiting != 0 || !st.PausedUntil.IsZero() {
			t.Fatalf("the status: %+v, want %d failed, none waiting and no pause", st, reports)
		}
		if charged, _ := publishCharges(t, e); charged != 0 {
			t.Fatalf("%d attempts still charged after every report was given up", charged)
		}
	})
}

// An attempt that is not surely over may still be waiting for GitHub's
// answer at another publisher sharing the store, so a call made meanwhile
// neither tries the report again nor gives its charge back: it goes on to
// the reports behind it.
func TestAReportWhoseAttemptMayStillBeAnsweredIsNotTriedAgainYet(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		sendReportText(t, e, "First", "Body.", 1)
		db.Clock.Advance(time.Second)
		sendReportText(t, e, "Second", "Body.", 2)
		gh.fail = []error{rejected(http.StatusBadGateway)}
		if _, err := p.Publish(context.Background()); err == nil {
			t.Fatal("a pass GitHub answered 502 reported no error")
		}
		db.Clock.Advance(time.Minute)
		if res := publish(t, p); res.Published != 1 {
			t.Fatalf("a pass a minute later: %+v, want only the second report published", res)
		}
		if st := readPublication(t, e, db.Clock.Now()); st.LastError != "" {
			t.Fatalf("the status once a report was published: %+v, want the error cleared", st)
		}
		if charged, live := publishCharges(t, e); charged != 2 || live != 1 {
			t.Fatalf("%d charges and %d reports waiting for an answer, want the first report's charge kept beside the second's", charged, live)
		}
		if titles := gh.created(); len(titles) != 1 || titles[0].Title != "Report: Second" || gh.creates != 2 {
			t.Fatalf("%d issues asked for, opened %+v, want the first report not asked for again yet", gh.creates, titles)
		}
		db.Clock.Advance(3 * time.Minute)
		if res := publish(t, p); res.Published != 1 {
			t.Fatalf("a pass once the attempt is surely over: %+v, want the first report published", res)
		}
		if charged, live := publishCharges(t, e); charged != 2 || live != 0 {
			t.Fatalf("%d charges and %d reports waiting for an answer, want one charge for each issue opened", charged, live)
		}
	})
}

// A pass that only held back a report whose attempt may still be answered
// says nothing about GitHub, so the status keeps the error of the attempt
// that failed instead of reading as a clean pass.
func TestAPassThatOnlyHeldBackAReportKeepsTheLastError(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		sendReportText(t, e, "Only", "Body.", 1)
		gh.fail = []error{rejected(http.StatusBadGateway)}
		if _, err := p.Publish(context.Background()); err == nil {
			t.Fatal("a pass GitHub answered 502 reported no error")
		}
		failed := readPublication(t, e, db.Clock.Now()).LastError
		if failed == "" {
			t.Fatal("the status after a 502 names no error")
		}
		db.Clock.Advance(time.Minute)
		if res, err := p.Publish(context.Background()); err != nil || res.Published != 0 || gh.creates != 1 {
			t.Fatalf("a pass a minute later: %+v, %v, %d issues asked for; want the report held back", res, err, gh.creates)
		}
		if st := readPublication(t, e, db.Clock.Now()); st.LastError != failed {
			t.Fatalf("the status after a pass that held the report back: %q, want the last error %q kept", st.LastError, failed)
		}
	})
}

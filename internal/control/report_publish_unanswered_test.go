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

// askedFor counts the issues asked for by title.
func askedFor(gh *reportGitHub) map[string]int {
	gh.mu.Lock()
	defer gh.mu.Unlock()
	n := map[string]int{}
	for _, title := range gh.asked {
		n[title]++
	}
	return n
}

// GitHub listing issues but answering every new one with an outage for an
// hour opens nothing and gives no report up: each report's unanswered
// attempt is looked for, found missing, and its charge taken back, so the
// budget holds at most one charge per report waiting for an answer and
// publishing is never paused for the day. A report is tried again after a
// wait that grows to five minutes, not on every pass, and once GitHub
// answers again every report is published once.
func TestReportsWhoseIssuesGitHubNeverAnswersKeepWaitingWithOneChargeEach(t *testing.T) {
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
			if charged, live := publishCharges(t, e); charged > live || charged > reports {
				t.Fatalf("pass %d: %d attempts charged with %d reports waiting for an answer", pass, charged, live)
			}
			db.Clock.Advance(time.Minute)
		}
		// Tried at once, three minutes later and then every five minutes
		// at most: 13 attempts of one report in an hour.
		for title, n := range askedFor(gh) {
			if n < 2 || n > 13 {
				t.Errorf("%q asked for %d times in an hour of outage, want it tried again but at most every five minutes", title, n)
			}
		}
		if waiting, failed := countKeys(t, e, "r/"), countKeys(t, e, "f/"); waiting != reports || failed != 0 {
			t.Fatalf("%d reports waiting and %d given up, want every report still waiting", waiting, failed)
		}
		if st := readPublication(t, e, db.Clock.Now()); st.Failed != 0 || st.Waiting != reports || !st.PausedUntil.IsZero() {
			t.Fatalf("the status: %+v, want %d waiting, none failed and no pause", st, reports)
		}
		gh.mu.Lock()
		gh.fail = nil
		gh.mu.Unlock()
		for range 3 {
			db.Clock.Advance(5 * time.Minute)
			publish(t, p)
		}
		if titles := gh.created(); len(titles) != reports {
			t.Fatalf("%d issues opened once GitHub answered again, want one for each of the %d reports", len(titles), reports)
		}
		if waiting := countKeys(t, e, "r/"); waiting != 0 {
			t.Fatalf("%d reports still waiting once GitHub answered again", waiting)
		}
		if charged, live := publishCharges(t, e); charged != reports || live != 0 {
			t.Fatalf("%d charges and %d reports waiting for an answer, want one charge for each issue opened", charged, live)
		}
	})
}

// Reports GitHub keeps failing on, as many as a call asks GitHub about,
// do not keep the report behind them from being published: a report held
// back while its next attempt is not due asks GitHub nothing and leaves
// room for the next. Each of them is tried again after a wait that grows
// to five minutes.
func TestReportsGitHubKeepsFailingOnDoNotHoldUpTheReportsBehind(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		p.Batch = 2
		for n, title := range []string{"First", "Second", "Third"} {
			sendReportText(t, e, title, "Body.", n)
			db.Clock.Advance(time.Second)
		}
		gh.down = map[string]bool{"Report: First": true, "Report: Second": true}
		for range 20 {
			_, _ = p.Publish(context.Background())
			db.Clock.Advance(time.Minute)
		}
		if titles := gh.created(); len(titles) != 1 || titles[0].Title != "Report: Third" {
			t.Fatalf("opened %+v, want the report behind those GitHub keeps failing on", titles)
		}
		if waiting := countKeys(t, e, "r/"); waiting != 2 {
			t.Fatalf("%d reports waiting, want the two GitHub keeps failing on", waiting)
		}
		// Tried at once, three minutes later and then every five minutes
		// at most: 5 attempts of one report in 20 minutes.
		for _, title := range []string{"Report: First", "Report: Second"} {
			if n := askedFor(gh)[title]; n < 2 || n > 5 {
				t.Errorf("%q asked for %d times in 20 minutes, want it tried again but at most every five minutes", title, n)
			}
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

// A spent budget does not keep the charge of an attempt that went
// unanswered and opened nothing until the report's next attempt is due:
// the lookup gives it back at once, and the report waiting behind is
// published on it.
func TestASpentBudgetGivesBackTheChargeOfAnAttemptThatOpenedNothing(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		p.Batch = control.ReportDailyPublishBudget + 10
		sendReportText(t, e, "Unanswered", "Body.", 0)
		for n := range control.ReportDailyPublishBudget {
			db.Clock.Advance(time.Millisecond)
			if code, _ := sendReportText(t, e, fmt.Sprint("Report ", n), "Body.", n+1); code != http.StatusAccepted {
				t.Fatalf("report %d: %d", n, code)
			}
		}
		gh.fail = []error{rejected(http.StatusBadGateway)}
		if _, err := p.Publish(context.Background()); err == nil {
			t.Fatal("a pass GitHub answered 502 reported no error")
		}
		db.Clock.Advance(time.Minute)
		publish(t, p)
		db.Clock.Advance(3 * time.Minute)
		gh.fail = []error{rejected(http.StatusBadGateway)}
		if _, err := p.Publish(context.Background()); err == nil {
			t.Fatal("a pass whose retry GitHub answered 502 reported no error")
		}
		// The retry is over three minutes later, but the next one is due
		// only five minutes after it.
		db.Clock.Advance(3 * time.Minute)
		res := publish(t, p)
		if charged, live := publishCharges(t, e); charged != control.ReportDailyPublishBudget-1 || live != 0 || !res.PausedUntil.IsZero() {
			t.Fatalf("%d charges, %d reports waiting for an answer, %+v; want the unanswered attempt's charge given back and no pause", charged, live, res)
		}
		db.Clock.Advance(time.Minute)
		if res := publish(t, p); res.Published != 1 {
			t.Fatalf("the next pass: %+v, want the report behind published on the charge given back", res)
		}
		if titles := gh.created(); titles[len(titles)-1].Title != "Report: Report 49" {
			t.Fatalf("opened last %q, want the report the budget had left waiting", titles[len(titles)-1].Title)
		}
	})
}

// A spent budget still lets a call look for the issue of an unanswered
// attempt that is surely over: when none was opened, the report is tried
// again on the charge its attempt holds, instead of that charge being kept
// for a day. A report with no attempt is not tried while the budget is
// spent.
func TestASpentBudgetStillSettlesAnUnansweredAttempt(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		p.Batch = control.ReportDailyPublishBudget + 10
		sendReportText(t, e, "Unanswered", "Body.", 0)
		for n := range control.ReportDailyPublishBudget {
			db.Clock.Advance(time.Millisecond)
			if code, _ := sendReportText(t, e, fmt.Sprint("Report ", n), "Body.", n+1); code != http.StatusAccepted {
				t.Fatalf("report %d: %d", n, code)
			}
		}
		gh.fail = []error{rejected(http.StatusBadGateway)}
		if _, err := p.Publish(context.Background()); err == nil {
			t.Fatal("a pass GitHub answered 502 reported no error")
		}
		db.Clock.Advance(time.Minute)
		if res := publish(t, p); res.Published != control.ReportDailyPublishBudget-1 || res.PausedUntil.IsZero() {
			t.Fatalf("the second pass: %+v, want the budget spent beside the unanswered attempt", res)
		}
		db.Clock.Advance(3 * time.Minute)
		res := publish(t, p)
		if res.Published != 1 || res.PausedUntil.IsZero() {
			t.Fatalf("a pass with the budget spent once the attempt is over: %+v, want the unanswered report published on its own charge", res)
		}
		if n := askedFor(gh)["Report: Report 49"]; n != 0 {
			t.Fatalf("a report with no attempt was asked for %d times while the budget was spent", n)
		}
		if charged, live := publishCharges(t, e); charged != control.ReportDailyPublishBudget || live != 0 {
			t.Fatalf("%d charges and %d reports waiting for an answer, want one charge for each issue opened", charged, live)
		}
		if waiting := countKeys(t, e, "r/"); waiting != 1 {
			t.Fatalf("%d reports waiting, want the one the budget left", waiting)
		}
	})
}

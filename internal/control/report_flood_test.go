package control_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// A flood of reports cannot make the factory open more than its daily
// budget of issues, nor keep a person's report out. Twenty-four addresses
// send as many reports as the route takes in the first hour, then one of
// them goes on at its own limit every hour for a day, while the publisher
// runs every minute as the control pass does. Within the day at most
// ReportDailyPublishBudget issues are opened, oldest report first, and
// then publishing pauses with the backlog shown; a report from a new
// address is accepted all the while, since the store keeps room.
func TestAReportFloodOpensAtMostTheDailyBudgetInReceiptOrder(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		start := db.Clock.Now()
		var accepted []string
		send := func(client, n int) {
			title := fmt.Sprintf("Flood %d from %d", n, client)
			if code, _ := sendReportText(t, e, title, "Body.", client); code == http.StatusAccepted {
				accepted = append(accepted, "Report: "+title)
			}
			db.Clock.Advance(100 * time.Millisecond)
		}
		genuine := false
		for minute := range 24 * 60 {
			if minute == 0 {
				for client := 1; client <= 24; client++ {
					for n := range 5 {
						send(client, n)
					}
				}
				if len(accepted) != 60 {
					t.Fatalf("%d reports accepted in the first hour from 24 addresses, want the overall limit of 60", len(accepted))
				}
			}
			if minute > 0 && minute%60 == 0 {
				for n := range 5 {
					send(1, minute+n)
				}
			}
			if minute == 12*60+30 {
				if code, _ := sendReportText(t, e, "Checkout fails", "A person's report.", 200); code != http.StatusAccepted {
					t.Fatalf("a person's report during the flood: %d, want it accepted", code)
				}
				genuine = true
			}
			publish(t, p)
			db.Clock.Advance(start.Add(time.Duration(minute+1) * time.Minute).Sub(db.Clock.Now()))
		}
		if !genuine {
			t.Fatal("the person's report was never sent")
		}
		issues := gh.created()
		if len(issues) != control.ReportDailyPublishBudget {
			t.Fatalf("%d issues opened in a day of flooding, want the daily budget of %d", len(issues), control.ReportDailyPublishBudget)
		}
		titles := make([]string, len(issues))
		for i, issue := range issues {
			titles[i] = issue.Title
		}
		if want := accepted[:len(issues)]; !slices.Equal(titles, want) {
			t.Fatalf("issues were not opened in the order their reports were received:\n got %s\nwant %s",
				strings.Join(titles, ", "), strings.Join(want, ", "))
		}
		st, err := control.ReadReportPublication(context.Background(), e.store, db.Clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		if want := len(accepted) + 1 - len(issues); st.Waiting != want || st.PausedUntil.IsZero() || !st.PausedUntil.After(db.Clock.Now()) {
			t.Fatalf("the status after a day of flooding: %+v, want %d waiting and publishing paused", st, want)
		}
		// Once the oldest publication is a day old, publishing goes on.
		db.Clock.Advance(st.PausedUntil.Sub(db.Clock.Now()))
		if res := publish(t, p); res.Published == 0 {
			t.Fatalf("publishing did not resume once the budget freed: %+v", res)
		}
	})
}

// A spent budget pauses publishing without dropping anything: the call
// opens nothing, says until when, and every report stays waiting.
func TestASpentBudgetPausesPublishingWithoutDroppingReports(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		p.Batch = control.ReportDailyPublishBudget + 10
		for n := range control.ReportDailyPublishBudget + 3 {
			if code, _ := sendReportText(t, e, fmt.Sprintf("Report %d", n), "Body.", n); code != http.StatusAccepted {
				t.Fatalf("report %d: %d", n, code)
			}
			db.Clock.Advance(time.Second)
		}
		first := db.Clock.Now()
		res := publish(t, p)
		if res.Published != control.ReportDailyPublishBudget || !res.PausedUntil.Equal(first.Add(24*time.Hour)) {
			t.Fatalf("a call with more waiting than the budget: %+v, want %d published and a pause until %s",
				res, control.ReportDailyPublishBudget, first.Add(24*time.Hour))
		}
		db.Clock.Advance(time.Hour)
		if res := publish(t, p); res.Published != 0 || res.PausedUntil.IsZero() {
			t.Fatalf("a call with the budget spent: %+v, want a pause", res)
		}
		if n := len(gh.created()); n != control.ReportDailyPublishBudget {
			t.Fatalf("%d issues opened, want %d", n, control.ReportDailyPublishBudget)
		}
		if n := countKeys(t, e, "r/"); n != 3 {
			t.Fatalf("%d reports wait with the budget spent, want 3 kept", n)
		}
	})
}

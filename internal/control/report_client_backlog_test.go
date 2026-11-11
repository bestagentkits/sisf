package control_test

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// One address cannot queue more than its share ahead of everyone else.
// It sends at its hourly limit for a day while the publisher opens one
// issue an hour: it never holds more than ten waiting reports, a report
// beyond them is refused as too many with a time to retry, and a person's
// report sent during the flood is published within the next eleven
// publications.
func TestOneAddressHoldsAtMostTenWaitingReports(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		p.Batch = 1
		const personTitle = "Report: Checkout fails"
		refused, publications, personAt := 0, 0, -1
		// personWaiting is 1 while the person's report waits, 0 otherwise.
		personWaiting := func() int {
			if personAt < 0 {
				return 0
			}
			for _, issue := range gh.created() {
				if issue.Title == personTitle {
					return 0
				}
			}
			return 1
		}
		for hour := range 24 {
			for n := range 5 {
				rec := postReportRecorded(t, e, fmt.Sprintf("Flood %d-%d", hour, n), 1)
				switch rec.Code {
				case http.StatusAccepted:
				case http.StatusTooManyRequests:
					refused++
					if after, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || after < 1 {
						t.Fatalf("a report beyond the address's share: Retry-After %q, want a time to retry", rec.Header().Get("Retry-After"))
					}
				default:
					t.Fatalf("flood report %d-%d: %d %s", hour, n, rec.Code, rec.Body)
				}
				if held := readPublication(t, e, db.Clock.Now()).Waiting - personWaiting(); held > 10 {
					t.Fatalf("hour %d: the address holds %d waiting reports, want at most 10", hour, held)
				}
				db.Clock.Advance(time.Minute)
			}
			if hour == 12 {
				if code, _ := sendReportText(t, e, "Checkout fails", "A person's report.", 200); code != http.StatusAccepted {
					t.Fatalf("a person's report during the flood: %d, want it accepted", code)
				}
				personAt = publications
			}
			publications += publish(t, p).Published
			db.Clock.Advance(time.Hour - 5*time.Minute)
		}
		if refused == 0 {
			t.Fatal("an address at its hourly limit for a day was never refused, want reports beyond its share refused")
		}
		for i, issue := range gh.created() {
			if issue.Title == personTitle {
				if waited := i + 1 - personAt; waited > 11 {
					t.Fatalf("the person's report was publication %d after it was sent, want within 11", waited)
				}
				return
			}
		}
		t.Fatal("the person's report was never published")
	})
}

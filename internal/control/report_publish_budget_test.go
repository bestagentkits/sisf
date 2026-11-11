package control_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// gatedIssues holds the first issue each publisher asks for until both
// asked, so both have read the budget before either opens an issue.
type gatedIssues struct {
	*reportGitHub
	once    sync.Once
	arrived *sync.WaitGroup
}

func (g *gatedIssues) CreateIssue(ctx context.Context, repo github.RepoRef, n github.NewIssue) (github.Issue, error) {
	g.once.Do(func() {
		g.arrived.Done()
		done := make(chan struct{})
		go func() { g.arrived.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	return g.reportGitHub.CreateIssue(ctx, repo, n)
}

// Two publishers over one store, as two control processes would be, open
// no more than the daily budget between them: the budget is checked and
// charged in the write that records each attempt, so the second one to
// write finds what the first spent.
func TestTwoPublishersTogetherKeepToTheDailyBudget(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		const sent = control.ReportDailyPublishBudget + 10
		for n := range sent {
			if code, _ := sendReportText(t, e, fmt.Sprintf("Report %d", n), "Body.", n); code != http.StatusAccepted {
				t.Fatalf("report %d: %d", n, code)
			}
		}
		arrived := &sync.WaitGroup{}
		arrived.Add(2)
		var wg sync.WaitGroup
		for range 2 {
			publisher := p
			publisher.Batch = sent
			publisher.Issues = &gatedIssues{reportGitHub: gh, arrived: arrived}
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 3 {
					if _, err := publisher.Publish(context.Background()); err != nil {
						t.Errorf("publish: %v", err)
						return
					}
				}
			}()
		}
		wg.Wait()
		if n := len(gh.created()); n != control.ReportDailyPublishBudget {
			t.Fatalf("two publishers opened %d issues, want the daily budget of %d", n, control.ReportDailyPublishBudget)
		}
		if res := publish(t, p); res.Published != 0 || res.PausedUntil.IsZero() {
			t.Fatalf("a call after the budget was spent: %+v, want a pause", res)
		}
		// Each issue was charged, so no more reports than issues left the
		// waiting ones.
		if st := readPublication(t, e, db.Clock.Now()); st.Waiting < sent-control.ReportDailyPublishBudget {
			t.Fatalf("the status: %+v, want at least %d still waiting", st, sent-control.ReportDailyPublishBudget)
		}
	})
}

// An attempt GitHub refused opened no issue, so it gives its charge back:
// a day still publishes the whole budget of issues when some reports were
// refused on the way.
func TestARefusedIssueIsNotChargedAgainstTheBudget(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e, gh, p := reportEnv(t, db)
		p.Batch = control.ReportDailyPublishBudget + 10
		for n := range control.ReportDailyPublishBudget + 1 {
			if code, _ := sendReportText(t, e, fmt.Sprintf("Report %d", n), "Body.", n); code != http.StatusAccepted {
				t.Fatalf("report %d: %d", n, code)
			}
		}
		gh.fail = []error{rejected(422), rejected(422), rejected(422)}
		// The first call takes as many reports as the budget allows.
		if res := publish(t, p); res.Rejected != 3 || res.Published != control.ReportDailyPublishBudget-3 {
			t.Fatalf("the first call: %+v, want 3 refused and the rest published", res)
		}
		if res := publish(t, p); res.Published != 3 || res.PausedUntil.IsZero() {
			t.Fatalf("the second call: %+v, want the three charges given back used and then a pause", res)
		}
		if n := len(gh.created()); n != control.ReportDailyPublishBudget {
			t.Fatalf("%d issues opened, want the daily budget of %d", n, control.ReportDailyPublishBudget)
		}
	})
}

// Retention is counted from each report's own receipt time, which its key
// keeps: a clock that jumps forward by less than the retention prunes no
// waiting report, neither on the next save nor on the periodic pass.
func TestAClockJumpWithinTheRetentionPrunesNoWaitingReport(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		e := newEnv(t, db)
		retention := 48 * time.Hour
		e.deps.Report = &control.ReportRoute{Redactor: redact.New(redact.Config{}), Retention: retention}
		e.restart(t)
		if code, _ := sendReportText(t, e, "Before the jump", "Body.", 1); code != http.StatusAccepted {
			t.Fatalf("the first report: %d", code)
		}
		db.Clock.Advance(retention - time.Minute)
		if err := e.server.PruneReports(context.Background()); err != nil {
			t.Fatal(err)
		}
		if code, _ := sendReportText(t, e, "After the jump", "Body.", 2); code != http.StatusAccepted {
			t.Fatalf("the second report: %d", code)
		}
		if n := countKeys(t, e, "r/"); n != 2 {
			t.Fatalf("%d reports wait after a jump within the retention, want both", n)
		}
	})
}

package wiring_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/state"
)

// The factory's status counts the reports waiting to be published, those
// given up after GitHub refused them and those pushed out of a full store, so the operator sees reports
// that will never become issues. It reads the store's counters, not the
// reports: a report given up is counted by the publisher as it moves it.
func TestTheStatusCountsReportsWaitingAndGivenUp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	sendReport(t, f, "Search is slow", "It takes a minute.")
	err := f.ControlStore.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		for _, e := range []state.KVEntry{
			{Namespace: control.ReportsNamespace, Key: "f/given-up", Value: []byte(`{}`)},
			{Namespace: control.ReportsNamespace, Key: "failed-count", Value: []byte("1")},
			{Namespace: control.ReportsNamespace, Key: "dropped-count", Value: []byte("4")},
		} {
			if _, err := tx.PutKV(ctx, e); err != nil {
				return err
			}
		}
		count, err := tx.GetKV(ctx, control.ReportsNamespace, "count")
		if err != nil {
			return err
		}
		count.Value = []byte("2")
		_, err = tx.PutKV(ctx, count)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sync, err := f.Control.GitHubSync(ctx)
	if err != nil || sync.ReportsWaiting != 1 || sync.ReportsFailed != 1 || sync.ReportsDropped != 4 {
		t.Fatalf("the status shows %d waiting, %d given up and %d dropped (%v), want 1, 1 and 4",
			sync.ReportsWaiting, sync.ReportsFailed, sync.ReportsDropped, err)
	}
	if !sync.ReportsPublishPausedUntil.IsZero() {
		t.Fatalf("the status shows publishing paused until %s with the budget unspent", sync.ReportsPublishPausedUntil)
	}
}

// Once the day's budget of report issues is spent, the factory's status
// shows the backlog and until when publishing pauses.
func TestTheStatusShowsPublishingPausedByTheDailyBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	extra := 3
	for n := range control.ReportDailyPublishBudget + extra {
		sendReport(t, f, fmt.Sprintf("Report %d", n), "Body.")
	}
	first := f.Clock.Now()
	for range control.ReportDailyPublishBudget/control.DefaultReportPublishBatch + 2 {
		f.Pass()
		f.Clock.Advance(time.Minute)
	}
	sync, err := f.Control.GitHubSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sync.ReportsWaiting != extra || sync.ReportsPublishPausedUntil.Before(first.Add(24*time.Hour)) {
		t.Fatalf("the status shows %d waiting and publishing paused until %s, want %d waiting and a pause of a day",
			sync.ReportsWaiting, sync.ReportsPublishPausedUntil, extra)
	}
}

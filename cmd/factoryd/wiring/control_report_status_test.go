package wiring_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/state"
)

// The factory's status counts the reports waiting to be published and
// those given up after GitHub refused them, so the operator sees reports
// that will never become issues.
func TestTheStatusCountsReportsWaitingAndGivenUp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	sendReport(t, f, "Search is slow", "It takes a minute.")
	err := f.ControlStore.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := tx.PutKV(ctx, state.KVEntry{Namespace: control.ReportsNamespace, Key: "f/given-up", Value: []byte(`{}`)})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sync, err := f.Control.GitHubSync(ctx)
	if err != nil || sync.ReportsWaiting != 1 || sync.ReportsFailed != 1 {
		t.Fatalf("the status shows %d waiting and %d given up (%v), want 1 and 1", sync.ReportsWaiting, sync.ReportsFailed, err)
	}
}

package wiring_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
)

// More issue changes within one second than one poll reads, as a bulk
// edit leaves them, are worked off over a few polls like any backlog: the
// polls do not keep reading the same first page of that second.
func TestABacklogWithinOneSecondIsWorkedOff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{Reconcile: wiring.ReconcileConfig{MaxPages: 1}})
	const bulk = 150
	f.GitHub.DropNext(bulk + 1)
	f.GitHub.HoldClock(true)
	for i := range bulk {
		f.OpenIssue("passer-by", "NONE", fmt.Sprintf("Idea %d", i), "")
	}
	f.GitHub.HoldClock(false)
	f.OpenIssue("passer-by", "NONE", "A later idea", "")
	for range 5 {
		if err := f.Control.ReconcileGitHub(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(reconciledIssues(t, f)); got != bulk+1 {
		t.Fatalf("the polls stored %d of %d changes", got, bulk+1)
	}
	if status := gitHubSync(t, f); status.Backlog {
		t.Fatal("the backlog is still shown after it was worked off")
	}
}

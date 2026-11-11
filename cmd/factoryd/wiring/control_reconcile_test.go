package wiring_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// failingSource is a GitHub that cannot list issues: it fails at once, or
// with hang set, answers only when the poll gives up.
type failingSource struct {
	hang  bool
	polls atomic.Int32
}

func (s *failingSource) PollIssues(ctx context.Context, _ github.RepoRef, _ github.ListIssuesOptions, _ string) (github.IssuePage, error) {
	s.polls.Add(1)
	if s.hang {
		<-ctx.Done()
		return github.IssuePage{}, core.NewError(core.CodeUnavailable, "GitHub did not answer").WithCause(ctx.Err())
	}
	return github.IssuePage{}, core.NewError(core.CodeUnavailable, "GitHub is down")
}

// pendingFor returns the IDs of the inbox entries not yet processed.
func pendingFor(t *testing.T, f *harness.Factory) []string {
	t.Helper()
	var ids []string
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entries, err := tx.PendingDeliveries(ctx, 100)
		for _, e := range entries {
			ids = append(ids, e.DeliveryID)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// A poll that fails or hangs does not keep the same pass from handing the
// webhooks to intake and performing their effects on GitHub, and a failing
// GitHub is asked again only after the interval.
func TestAFailedReconciliationDoesNotStopTheRestOfThePass(t *testing.T) {
	t.Parallel()
	for name, source := range map[string]*failingSource{"failing": {}, "hanging": {hang: true}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := harness.New(t, harness.Options{Reconcile: wiring.ReconcileConfig{Source: source, Timeout: 50 * time.Millisecond}})
			number := f.OpenIssue("maintainer", "MEMBER", "Health check fails", bugBody, github.LabelReady, github.LabelKindBug)

			started := time.Now()
			err := f.Control.Pass(context.Background())
			if err == nil || !strings.Contains(err.Error(), "reconciling with GitHub failed") {
				t.Fatalf("the pass hid the failed poll: %v", err)
			}
			if elapsed := time.Since(started); elapsed > 10*time.Second {
				t.Fatalf("the pass waited %s for GitHub", elapsed)
			}
			if state := f.Record(number).State; state != core.StateReady {
				t.Fatalf("the webhook was not handed to intake in the same pass: issue is %s", state)
			}
			comments, err := f.GitHub.Comments(f.Repo, number)
			if err != nil || len(comments) == 0 {
				t.Fatalf("the pass performed no effect on GitHub: %v, %v", comments, err)
			}
			if err := f.Control.Pass(context.Background()); err != nil {
				t.Fatalf("a pass inside the interval asked the failing GitHub again: %v", err)
			}
			f.Clock.Advance(wiring.DefaultReconcileInterval)
			if err := f.Control.Pass(context.Background()); err == nil {
				t.Fatal("the pass after the interval did not poll again")
			}
			if polls := source.polls.Load(); polls != 2 {
				t.Fatalf("GitHub was polled %d times, want once per interval", polls)
			}
		})
	}
}

// One change of an issue that both a webhook and the poll see is stored in
// the inbox once, and an issue seen by both makes one run.
func TestAnIssueSeenByWebhookAndPollIsProcessedOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})

	// The webhook arrives first: the poll finds nothing it lacks.
	first := f.OpenIssue("maintainer", "MEMBER", "Health check fails", bugBody, github.LabelReady, github.LabelKindBug)
	webhooks := pendingFor(t, f)
	if len(webhooks) != 1 {
		t.Fatalf("the webhook stored %v", webhooks)
	}
	if err := f.Control.ReconcileGitHub(ctx); err != nil {
		t.Fatal(err)
	}
	if got := pendingFor(t, f); len(got) != 1 || got[0] != webhooks[0] {
		t.Fatalf("the poll stored a change the webhook had already stored: %v", got)
	}

	// The poll comes first: it stores the change once however often it
	// polls, and the late webhook makes no second run.
	f.GitHub.DropNext(1)
	second := f.OpenIssue("maintainer", "MEMBER", "Health check is slow", bugBody, github.LabelReady, github.LabelKindBug)
	for range 2 {
		if err := f.Control.ReconcileGitHub(ctx); err != nil {
			t.Fatal(err)
		}
	}
	polled := pendingFor(t, f)
	if len(polled) != 2 || !strings.HasPrefix(polled[1], "reconcile-") {
		t.Fatalf("the poll did not store the lost change exactly once: %v", polled)
	}
	deliveries := f.GitHub.Deliveries()
	if err := f.GitHub.Redeliver(deliveries[len(deliveries)-1].ID); err != nil {
		t.Fatal(err)
	}

	f.Settle(4)
	for _, number := range []int{first, second} {
		if runs := f.Runs(number); len(runs) != 1 {
			t.Fatalf("issue %d has %d runs, want 1", number, len(runs))
		}
		if started := f.Agent.Started(f.Issue(number)); len(started) != 1 {
			t.Fatalf("the agent was started %d times for issue %d, want 1", len(started), number)
		}
		scopes := 0
		comments, err := f.GitHub.Comments(f.Repo, number)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range comments {
			if strings.Contains(c.Body, "### Scope of Work v1") {
				scopes++
			}
		}
		if scopes != 1 {
			t.Fatalf("issue %d was scoped %d times", number, scopes)
		}
	}
}

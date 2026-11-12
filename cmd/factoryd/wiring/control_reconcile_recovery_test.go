package wiring_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// lateSource lists issues through a client of the harness's fake GitHub
// made once the factory exists, and can blank the update time of one issue,
// as a listing whose entry fails validation shows it.
type lateSource struct {
	inner github.IssueSource
	bad   int
}

func (s *lateSource) PollIssues(ctx context.Context, repo github.RepoRef, opts github.ListIssuesOptions, etag string) (github.IssuePage, error) {
	page, err := s.inner.PollIssues(ctx, repo, opts, etag)
	for i := range page.Issues {
		if page.Issues[i].Ref.Number == s.bad {
			page.Issues[i].UpdatedAt = time.Time{}
		}
	}
	return page, err
}

// connect points s at the harness's fake GitHub.
func (s *lateSource) connect(t *testing.T, f *harness.Factory) {
	t.Helper()
	client, err := wiring.NewGitHubClient(wiring.GitHubSettings{APIURL: f.APIURL()}, core.NewSecretValue([]byte("harness-github-token")), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.inner = client
}

// reconciledIssues returns the issue numbers of the reconciled inbox
// entries not yet processed.
func reconciledIssues(t *testing.T, f *harness.Factory) map[int]bool {
	t.Helper()
	out := map[int]bool{}
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entries, err := tx.PendingDeliveries(ctx, 1000)
		for _, e := range entries {
			if rest, ok := strings.CutPrefix(e.DeliveryID, "reconcile-"); ok {
				var number int
				if _, err := fmt.Sscanf(rest, "%d-", &number); err == nil {
					out[number] = true
				}
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func gitHubSync(t *testing.T, f *harness.Factory) (out struct {
	Backlog  bool
	Position time.Time
	Retrying []int
	Skipped  []int
}) {
	t.Helper()
	sync, err := f.Control.GitHubSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out.Backlog, out.Position = sync.Backlog, sync.Position
	for _, r := range sync.Retrying {
		out.Retrying = append(out.Retrying, r.Issue)
	}
	for _, s := range sync.Skipped {
		out.Skipped = append(out.Skipped, s.Issue)
	}
	return out
}

// A change that fails validation holds up none of the changes after it:
// they are stored at once. It is read again by the next polls, with the
// position kept before it, and after a bounded number of polls it is given
// up and listed in the factory's status for the Owner, never dropped
// silently.
func TestAChangeThatFailsValidationHoldsUpNoOtherAndIsShownOnceGivenUp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := &lateSource{}
	f := harness.New(t, harness.Options{Reconcile: wiring.ReconcileConfig{Source: src}})
	src.connect(t, f)
	f.GitHub.DropNext(3)
	first := f.OpenIssue("maintainer", "MEMBER", "One", bugBody, github.LabelReady, github.LabelKindBug)
	src.bad = f.OpenIssue("maintainer", "MEMBER", "Two", bugBody, github.LabelReady, github.LabelKindBug)
	last := f.OpenIssue("maintainer", "MEMBER", "Three", bugBody, github.LabelReady, github.LabelKindBug)

	if err := f.Control.ReconcileGitHub(ctx); err != nil {
		t.Fatalf("a change that fails validation failed the poll: %v", err)
	}
	if got := reconciledIssues(t, f); !got[first] || !got[last] || got[src.bad] {
		t.Fatalf("stored %v; want issues %d and %d stored around the bad change", got, first, last)
	}
	status := gitHubSync(t, f)
	if len(status.Retrying) != 1 || status.Retrying[0] != src.bad || len(status.Skipped) != 0 {
		t.Fatalf("status = %+v; the bad change must be retried", status)
	}
	held := status.Position
	for range 3 {
		if err := f.Control.ReconcileGitHub(ctx); err != nil {
			t.Fatal(err)
		}
		if status := gitHubSync(t, f); !status.Position.Equal(held) {
			t.Fatalf("the position moved past a change still being retried: %s, want %s", status.Position, held)
		}
	}
	if err := f.Control.ReconcileGitHub(ctx); err != nil {
		t.Fatal(err)
	}
	status = gitHubSync(t, f)
	if len(status.Retrying) != 0 || len(status.Skipped) != 1 || status.Skipped[0] != src.bad {
		t.Fatalf("status = %+v; the bad change must be given up and shown", status)
	}
	if !status.Position.After(held) {
		t.Fatal("the position did not move on once the bad change was given up")
	}
	f.Settle(4)
	for _, number := range []int{first, last} {
		if runs := f.Runs(number); len(runs) != 1 {
			t.Fatalf("issue %d has %d runs, want 1", number, len(runs))
		}
	}
}

// More changes than one poll reads are taken in over several polls, oldest
// first: nothing is dropped, and the backlog shows in the factory's status
// until it is worked off.
func TestAReconciliationBacklogIsWorkedOffWithoutDroppingChanges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{Reconcile: wiring.ReconcileConfig{MaxPages: 1}})
	const issues = 150
	f.GitHub.DropNext(issues)
	for i := range issues {
		f.OpenIssue("passer-by", "NONE", fmt.Sprintf("Idea %d", i), "")
	}
	if err := f.Control.ReconcileGitHub(ctx); err != nil {
		t.Fatalf("a long listing failed the poll: %v", err)
	}
	if got := len(reconciledIssues(t, f)); got != 100 {
		t.Fatalf("the first poll stored %d changes, want its bound of 100", got)
	}
	if status := gitHubSync(t, f); !status.Backlog {
		t.Fatal("the factory's status does not show the backlog")
	}
	if err := f.Control.ReconcileGitHub(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(reconciledIssues(t, f)); got != issues {
		t.Fatalf("the polls stored %d of %d changes", got, issues)
	}
	if status := gitHubSync(t, f); status.Backlog {
		t.Fatal("the backlog is still shown after it was worked off")
	}
}

// On an install without webhooks the factory's own comments and labels are
// not brought back by polling as changes for intake.
func TestPollingDoesNotBringBackTheFactorysOwnEdits(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{OperatorToken: true})
	number := f.OpenIssue("maintainer", "MEMBER", "Health check fails", "The health endpoint is broken.", github.LabelReady, github.LabelKindBug)
	f.Pass()
	if got := reconciledIssues(t, f); !got[number] {
		t.Fatalf("the new issue was not reconciled: %v", got)
	}
	f.Pass()
	if f.Record(number).State != core.StateClarifying {
		t.Fatalf("the incomplete issue is %s, want it asked", f.Record(number).State)
	}
	for range 3 {
		f.Clock.Advance(wiring.DefaultReconcileInterval)
		f.Pass()
		if got := reconciledIssues(t, f); len(got) != 0 {
			t.Fatalf("polling brought back the factory's own edit as a change: %v", got)
		}
	}
	if asked := countComments(t, f, number, "More information needed"); asked != 1 {
		t.Fatalf("the question was asked %d times", asked)
	}
}

// A person's change whose webhook was lost just before an edit of the
// factory is still found by the next poll: the edit does not hide it.
func TestAPersonsLostChangeBeforeAFactoryEditIsStillFound(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Health check fails", "The health endpoint is broken.", github.LabelReady, github.LabelKindBug)
	f.Pass()
	if f.Record(number).State != core.StateClarifying {
		t.Fatalf("the incomplete issue is %s, want it asked", f.Record(number).State)
	}
	// The wait ends: this pass queues the parking, and polls.
	f.Clock.Advance(72 * time.Hour)
	f.Pass()
	// A person comments and the webhook is lost; the next pass puts the
	// parking on GitHub before any poll.
	f.GitHub.DropNext(1)
	f.Comment(number, "maintainer", "MEMBER", "It also fails at night.")
	f.Pass()
	if got := countComments(t, f, number, "Parked"); got != 1 {
		t.Fatalf("the parking was not put on GitHub: %d comments", got)
	}
	f.Clock.Advance(wiring.DefaultReconcileInterval)
	if err := f.Control.ReconcileGitHub(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := reconciledIssues(t, f); !got[number] {
		t.Fatal("the person's change was hidden by the factory's edit after it")
	}
}

// countComments counts the comments on issue number that contain text.
func countComments(t *testing.T, f *harness.Factory, number int, text string) int {
	t.Helper()
	comments, err := f.GitHub.Comments(f.Repo, number)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range comments {
		if strings.Contains(c.Body, text) {
			n++
		}
	}
	return n
}

package wiring_test

import (
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// clarifyTimeout is how long the sample manifest waits for an answer.
const clarifyTimeout = 72 * time.Hour

// parkedIssue opens an incomplete request, lets the wait for an answer run
// out and returns the issue once it carries sisf:needs-info on GitHub.
func parkedIssue(t *testing.T, f *harness.Factory) int {
	t.Helper()
	number := f.OpenIssue("maintainer", "MEMBER", "Health check fails", "The health endpoint is broken.", github.LabelReady, github.LabelKindBug)
	for range 2 {
		f.Pass()
	}
	if f.Record(number).State != core.StateClarifying {
		t.Fatalf("the incomplete issue is %s, want it asked", f.Record(number).State)
	}
	f.Clock.Advance(clarifyTimeout)
	for range 2 {
		f.Pass()
	}
	issue, err := f.GitHub.Issue(f.Repo, number)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(issue.Labels, github.LabelNeedsInfo) {
		t.Fatalf("the issue was not parked on GitHub: %+v", issue.Labels)
	}
	return number
}

// resumedAfterRemoval removes sisf:needs-info as a person and reports
// whether the factory asked again within a few polls.
func resumedAfterRemoval(t *testing.T, f *harness.Factory, number int, loseWebhook bool) bool {
	t.Helper()
	if loseWebhook {
		f.GitHub.DropNext(1)
	}
	if err := f.GitHub.RemoveLabel(f.Repo, number, f.GitHub.Person("maintainer", "MEMBER"), github.LabelNeedsInfo); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		f.Clock.Advance(wiring.DefaultReconcileInterval)
		f.Pass()
	}
	return countComments(t, f, number, "More information needed") == 2
}

// On an install that takes no webhooks a parked issue resumes once a
// person removed the label: polling finds the issue without it.
func TestAParkedIssueResumesOnAnInstallWithoutWebhooks(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{OperatorToken: true})
	number := parkedIssue(t, f)
	if !resumedAfterRemoval(t, f, number, false) {
		t.Fatal("the parked issue did not resume after the person removed the label")
	}
}

// With webhooks, a parked issue resumes even when the webhook of the label's
// removal was lost.
func TestAParkedIssueResumesWhenTheRemovalWebhookIsLost(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := parkedIssue(t, f)
	if !resumedAfterRemoval(t, f, number, true) {
		t.Fatal("the parked issue did not resume after the removal's webhook was lost")
	}
}

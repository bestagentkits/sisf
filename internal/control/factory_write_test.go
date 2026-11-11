package control_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// A change found at the update time of the factory's recorded edit is taken
// for that edit only when its content and the comments confirm it; any
// doubt, a failed confirmation included, stores the change.
func TestAReconciledChangeIsTakenForTheFactorysEditOnlyWhenConfirmed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := github.RepoRef{Owner: "acme", Name: "app"}
	at := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	issue := func(number int, updated time.Time, labels ...string) github.Issue {
		is := github.Issue{Ref: repo.Issue(number), Title: "t", Body: "b", State: github.IssueOpen, UpdatedAt: updated}
		for _, l := range labels {
			is.Labels = append(is.Labels, github.Label{Name: l})
		}
		return is
	}
	event := func(is github.Issue) github.WebhookEvent {
		return github.WebhookEvent{DeliveryID: "reconcile-" + is.UpdatedAt.String() + is.Ref.String(), Type: "issues",
			Action: "reconciled", InstallationID: 1, Repo: repo, Issue: &is, ReceivedAt: at}
	}
	confirmed := func(context.Context, core.IssueRef, time.Time) (bool, error) { return true, nil }
	cases := []struct {
		name    string
		listed  func(number int) github.Issue
		confirm control.ConfirmOwnEdit
		stored  bool
	}{
		{"own edit", func(n int) github.Issue { return issue(n, at.Add(time.Second), "sisf:needs-info") }, confirmed, false},
		{"another label", func(n int) github.Issue { return issue(n, at.Add(time.Second), "sisf:needs-info", "kind:bug") }, confirmed, true},
		{"a person's comment", func(n int) github.Issue { return issue(n, at.Add(time.Second), "sisf:needs-info") },
			func(context.Context, core.IssueRef, time.Time) (bool, error) { return false, nil }, true},
		{"no confirmation", func(n int) github.Issue { return issue(n, at.Add(time.Second), "sisf:needs-info") },
			func(context.Context, core.IssueRef, time.Time) (bool, error) {
				return false, errors.New("GitHub is down")
			}, true},
	}
	withServer(t, func(t *testing.T, e *env) {
		for i, tc := range cases {
			number := i + 1
			taken := issue(number, at)
			if _, err := control.AcceptReconciledIssue(ctx, e.store, event(taken), confirmed); err != nil {
				t.Fatal(err)
			}
			if err := control.RecordFactoryWrite(ctx, e.store, taken, issue(number, at.Add(time.Second), "sisf:needs-info"), true); err != nil {
				t.Fatal(err)
			}
			stored, err := control.AcceptReconciledIssue(ctx, e.store, event(tc.listed(number)), tc.confirm)
			if err != nil || stored != tc.stored {
				t.Errorf("%s: stored = %v, %v; want %v", tc.name, stored, err, tc.stored)
			}
		}
	})
}

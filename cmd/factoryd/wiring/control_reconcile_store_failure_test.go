package wiring

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// listedSource lists fixed issues the way GitHub does: those updated at or
// after the second of since, oldest first.
type listedSource struct{ issues []github.Issue }

func (s listedSource) PollIssues(_ context.Context, _ github.RepoRef, opts github.ListIssuesOptions, _ string) (github.IssuePage, error) {
	var page github.IssuePage
	for _, is := range s.issues {
		if !is.UpdatedAt.Before(opts.Since.Truncate(time.Second)) {
			page.Issues = append(page.Issues, is)
			page.Through = is.UpdatedAt
		}
	}
	return page, nil
}

// A failure of the store partway through a poll keeps the position, in
// memory and stored, at the last change actually stored: the next poll
// stores the change that failed and every one after it.
func TestAFailedSaveKeepsThePositionAtTheLastStoredChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := openTestStore(t, filepath.Join(t.TempDir(), "factory.db"), core.ZoneControl)
	if err != nil {
		t.Fatal(err)
	}
	repo := github.RepoRef{Owner: "acme", Name: "app"}
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	var issues []github.Issue
	for n := 1; n <= 3; n++ {
		issues = append(issues, github.Issue{Ref: core.IssueRef{Owner: "acme", Repo: "app", Number: n},
			State: github.IssueOpen, UpdatedAt: start.Add(time.Duration(n) * time.Minute)})
	}
	clock := sqlstoretest.NewClock(start.Add(time.Hour))
	cfg, err := ReconcileConfig{Source: listedSource{issues}}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	r := newReconciler(cfg, repo, 7, store, clock)
	stored := map[int]int{}
	failOn := 2
	r.accept = func(_ context.Context, ev github.WebhookEvent) (bool, error) {
		if ev.Issue.Ref.Number == failOn {
			failOn = 0
			return false, core.NewError(core.CodeUnavailable, "the database is gone")
		}
		stored[ev.Issue.Ref.Number]++
		return true, nil
	}
	c := &Control{cfg: ControlConfig{Clock: clock}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), reconcile: r}

	if err := c.ReconcileGitHub(ctx); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("the failed save was not reported: %v", err)
	}
	if stored[1] != 1 || stored[2] != 0 || stored[3] != 0 {
		t.Fatalf("stored %v; the poll must stop at the change it could not store", stored)
	}
	saved, err := r.loadState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Since.Equal(issues[0].UpdatedAt) || !r.poller.Since().Equal(issues[0].UpdatedAt) {
		t.Fatalf("position stored %s, in memory %s; want the last stored change's %s", saved.Since, r.poller.Since(), issues[0].UpdatedAt)
	}
	if saved.LastError == "" {
		t.Fatal("the failure is not kept for the factory's status")
	}

	clock.Advance(cfg.Interval)
	if err := c.ReconcileGitHub(ctx); err != nil {
		t.Fatal(err)
	}
	if stored[1] != 1 || stored[2] != 1 || stored[3] != 1 {
		t.Fatalf("stored %v; the next poll must store the rest, each once", stored)
	}
	if saved, _ = r.loadState(ctx); !saved.Since.Equal(issues[2].UpdatedAt) || saved.LastError != "" {
		t.Fatalf("after the retry the stored state is %+v", saved)
	}
}

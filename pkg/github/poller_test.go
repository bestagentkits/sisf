package github_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/github"
)

func numbers(issues []github.Issue) []int {
	out := make([]int, len(issues))
	for i, is := range issues {
		out[i] = is.Ref.Number
	}
	return out
}

// pollAll polls and acknowledges everything found, as a caller that stored
// every change does.
func pollAll(p *github.Poller) ([]github.Issue, error) {
	changes, err := p.Poll(bg)
	if err == nil {
		p.Ack(changes, len(changes.Issues))
	}
	return changes.Issues, err
}

func TestPollerReportsOnlyWhatChangedAndAnIdleRepositoryCostsANotModified(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	alice := f.person("alice")
	mustOpen(t, f, alice, "one")
	mustOpen(t, f, alice, "two")
	p := github.NewPoller(f.client, repoRef, time.Time{})

	first, err := pollAll(p)
	if err != nil || len(first) != 2 {
		t.Fatalf("first poll = %v, %v; a fresh poller reconciles everything", numbers(first), err)
	}
	// GitHub's since is inclusive: the newest issue comes back, and must be
	// recognized as already reported.
	again, err := pollAll(p)
	if err != nil || len(again) != 0 {
		t.Fatalf("second poll = %v, %v; nothing changed", numbers(again), err)
	}
	idle, err := pollAll(p)
	if err != nil || len(idle) != 0 {
		t.Fatalf("idle poll = %v, %v", numbers(idle), err)
	}
	reqs := f.fake.Requests()
	if last := reqs[len(reqs)-1]; last.Status != http.StatusNotModified {
		t.Fatalf("the idle poll answered %d, want 304", last.Status)
	}

	if err := f.fake.AddLabels(testRepo, 1, alice, github.LabelReady); err != nil {
		t.Fatal(err)
	}
	changed, err := pollAll(p)
	if err != nil || len(changed) != 1 || changed[0].Ref.Number != 1 || !changed[0].HasLabel(github.LabelReady) {
		t.Fatalf("after a label change: %v, %v", numbers(changed), err)
	}
	if empty, _ := pollAll(p); len(empty) != 0 {
		t.Fatalf("a change is reported once, got %v again", numbers(empty))
	}
}

func TestPollerPositionOnlyAdvancesAfterGitHubAnswered(t *testing.T) {
	t.Parallel()
	f := newFactory(t, func(c *github.ClientConfig) { c.MaxAttempts = 1 })
	alice := f.person("alice")
	p := github.NewPoller(f.client, repoRef, time.Time{})
	mustOpen(t, f, alice, "one")
	f.fake.FailNext(1, http.StatusBadGateway)
	if got, err := pollAll(p); err == nil || got != nil {
		t.Fatalf("a failed poll = %v, %v; it must report the failure", numbers(got), err)
	}
	if !p.Since().IsZero() {
		t.Fatal("the position moved although nothing was read")
	}
	if got, err := pollAll(p); err != nil || len(got) != 1 {
		t.Fatalf("the retry lost the change: %v, %v", numbers(got), err)
	}
	if p.Since().IsZero() {
		t.Fatal("the position must advance to the newest update stored")
	}
}

// A poll moves nothing by itself. The position advances only through the
// changes the caller acknowledges as stored: one it failed to store, and
// everything after it, is read again.
func TestPollerPositionOnlyAdvancesThroughWhatTheCallerStored(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	alice := f.person("alice")
	for i := range 3 {
		mustOpen(t, f, alice, fmt.Sprintf("issue %d", i))
	}
	p := github.NewPoller(f.client, repoRef, time.Time{})
	changes, err := p.Poll(bg)
	if err != nil || len(changes.Issues) != 3 {
		t.Fatalf("poll = %v, %v", numbers(changes.Issues), err)
	}
	if !p.Since().IsZero() {
		t.Fatalf("a poll nobody acknowledged moved the position to %s", p.Since())
	}
	// The caller stored the first change and failed on the second.
	p.Ack(changes, 1)
	if !p.Since().Equal(changes.Issues[0].UpdatedAt) {
		t.Fatalf("position = %s, want the stored change's %s", p.Since(), changes.Issues[0].UpdatedAt)
	}
	again, err := pollAll(p)
	if err != nil || len(again) != 2 || again[0].Ref.Number != 2 || again[1].Ref.Number != 3 {
		t.Fatalf("the next poll = %v, %v; want the change that was not stored and the one after it", numbers(again), err)
	}
	// A restart from the stored position reads them again as well.
	restarted := github.NewPoller(f.client, repoRef, changes.Issues[0].UpdatedAt)
	if got, err := pollAll(restarted); err != nil || len(got) < 2 {
		t.Fatalf("after a restart: %v, %v", numbers(got), err)
	}
}

func TestPollerResumesFromAPersistedPositionAndHonoursLabels(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	alice := f.person("alice")
	mustOpen(t, f, alice, "old", github.LabelReady)
	p := github.NewPoller(f.client, repoRef, time.Time{}, github.LabelReady)
	if got, _ := pollAll(p); len(got) != 1 {
		t.Fatalf("labelled issues = %v", numbers(got))
	}
	mustOpen(t, f, alice, "unlabelled")
	mustOpen(t, f, alice, "new", github.LabelReady)

	restarted := github.NewPoller(f.client, repoRef, p.Since(), github.LabelReady)
	got, err := pollAll(restarted)
	if err != nil || len(got) == 0 || got[len(got)-1].Ref.Number != 3 {
		t.Fatalf("after a restart: %v, %v", numbers(got), err)
	}
	for _, is := range got {
		if !is.HasLabel(github.LabelReady) {
			t.Fatalf("issue %d does not carry the label the poller filters on", is.Ref.Number)
		}
	}
}

// A listing longer than the poller's bound is read oldest first up to the
// bound; the next poll continues after it, so a long history is taken in
// over several polls and nothing is dropped.
func TestABoundedPollerReadsTheOldestChangesAndContinuesWithTheRest(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	alice := f.person("alice")
	for i := range 250 {
		mustOpen(t, f, alice, fmt.Sprintf("issue %d", i))
	}
	p := github.NewPoller(f.client, repoRef, time.Time{}).LimitPages(1)
	seen := map[int]bool{}
	for poll := 1; ; poll++ {
		changes, err := p.Poll(bg)
		if err != nil {
			t.Fatal(err)
		}
		if poll > 1 && len(changes.Issues) > 0 && seen[changes.Issues[0].Ref.Number] {
			t.Fatalf("poll %d reported issue %d again", poll, changes.Issues[0].Ref.Number)
		}
		for i, is := range changes.Issues {
			seen[is.Ref.Number] = true
			if i > 0 && is.UpdatedAt.Before(changes.Issues[i-1].UpdatedAt) {
				t.Fatal("changes must come oldest update first")
			}
		}
		p.Ack(changes, len(changes.Issues))
		if !changes.More {
			if poll != 3 {
				t.Fatalf("250 issues took %d polls of one page, want 3", poll)
			}
			break
		}
		if poll > 3 {
			t.Fatal("the bounded poller made no progress")
		}
	}
	if len(seen) != 250 {
		t.Fatalf("the polls found %d of 250 issues", len(seen))
	}
}

// changingSource changes an issue on the fake after the first request of a
// poll, as a person working while a long listing is read does.
type changingSource struct {
	inner  github.IssueSource
	change func()
	calls  int
}

func (s *changingSource) PollIssues(ctx context.Context, repo github.RepoRef, opts github.ListIssuesOptions, etag string) (github.IssuePage, error) {
	page, err := s.inner.PollIssues(ctx, repo, opts, etag)
	if s.calls++; s.calls == 1 {
		s.change()
	}
	return page, err
}

// An issue that changes while a listing of several pages is read moves to
// its end; no other issue is shifted out of the pages read.
func TestAChangeDuringAPollShiftsNoIssueOutOfIt(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	alice := f.person("alice")
	for i := range 150 {
		mustOpen(t, f, alice, fmt.Sprintf("issue %d", i))
	}
	src := &changingSource{inner: f.client, change: func() {
		if err := f.fake.AddLabels(testRepo, 1, alice, github.LabelReady); err != nil {
			t.Error(err)
		}
	}}
	got, err := pollAll(github.NewPoller(src, repoRef, time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for _, is := range got {
		seen[is.Ref.Number] = true
	}
	if len(seen) != 150 {
		t.Fatalf("the poll found %d of 150 issues; a change during it shifted some out", len(seen))
	}
}

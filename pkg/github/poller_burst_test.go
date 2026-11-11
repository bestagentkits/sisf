package github_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// burst opens n issues whose updates share one second, as a bulk edit on
// GitHub leaves them, and then one issue a second later.
func burst(t *testing.T, f *factory, n int) (later int) {
	t.Helper()
	alice := f.person("alice")
	f.fake.HoldClock(true)
	for i := range n {
		mustOpen(t, f, alice, fmt.Sprintf("bulk %d", i))
	}
	f.fake.HoldClock(false)
	return mustOpen(t, f, alice, "later")
}

// pollUntilDone polls and acknowledges everything, calling between after
// each poll, until a poll reads the listing to its end, and returns every
// issue number seen. It fails when that takes more than bound polls.
func pollUntilDone(t *testing.T, p *github.Poller, bound int, between func(poll int)) map[int]bool {
	t.Helper()
	seen := map[int]bool{}
	for poll := 1; poll <= bound; poll++ {
		changes, err := p.Poll(bg)
		if err != nil {
			t.Fatal(err)
		}
		for _, is := range changes.Issues {
			seen[is.Ref.Number] = true
		}
		p.Ack(changes, len(changes.Issues))
		if !changes.More {
			return seen
		}
		between(poll)
	}
	t.Fatalf("the poller still has a backlog after %d polls; it found %d issues", bound, len(seen))
	return nil
}

// More updates within one second than one poll reads are stepped through
// by page across polls: the poller does not start that second over at its
// first page on every poll, which would never get past it.
func TestAPollerGetsPastMoreUpdatesInOneSecondThanOnePollReads(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	later := burst(t, f, 150)
	p := github.NewPoller(f.client, repoRef, time.Time{}).LimitPages(1)
	seen := pollUntilDone(t, p, 6, func(int) {})
	if len(seen) != 151 || !seen[later] {
		t.Fatalf("the polls found %d of 151 issues (the later one: %v)", len(seen), seen[later])
	}
}

// An issue of a one-second burst that changes while the burst is stepped
// through by page moves to the end of the listing and shifts the issues
// after it back one place, so one of them falls onto a page already read.
// The poller notices the moved issue and reads the burst again: nothing is
// skipped.
func TestAChangeDuringAWalkThroughOneSecondSkipsNoIssue(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	burst(t, f, 250)
	p := github.NewPoller(f.client, repoRef, time.Time{}).LimitPages(1)
	changed := false
	seen := pollUntilDone(t, p, 20, func(poll int) {
		// By the second poll the walk is at the burst's second page.
		if poll == 2 && !changed {
			changed = true
			if err := f.fake.AddLabels(testRepo, 1, f.person("bob"), github.LabelReady); err != nil {
				t.Fatal(err)
			}
		}
	})
	if len(seen) != 251 {
		var missing []int
		for n := 1; n <= 251; n++ {
			if !seen[n] {
				missing = append(missing, n)
			}
		}
		t.Fatalf("the polls found %d of 251 issues; missing %v", len(seen), missing)
	}
}

// A pull request takes a place in the listing like an issue, so one that
// changes during the walk shifts the issues after it just the same.
func TestAPullRequestChangedDuringAWalkThroughOneSecondSkipsNoIssue(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	alice := f.person("alice")
	f.fake.HoldClock(true)
	pull, err := f.fake.OpenPullRequest(testRepo, alice, fakePull())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 249 {
		mustOpen(t, f, alice, fmt.Sprintf("bulk %d", i))
	}
	f.fake.HoldClock(false)
	p := github.NewPoller(f.client, repoRef, time.Time{}).LimitPages(1)
	seen := pollUntilDone(t, p, 20, func(poll int) {
		if poll == 2 {
			if err := f.fake.AddLabels(testRepo, pull, alice, github.LabelReady); err != nil {
				t.Fatal(err)
			}
		}
	})
	if len(seen) != 249 {
		t.Fatalf("the polls found %d of 249 issues", len(seen))
	}
}

// An issue that leaves the listing altogether while a one-second burst is
// walked by page, as a deleted or transferred issue does, or one that loses
// a label the poller filters on, never shows up again later in it, yet it
// shifts the issues after it back one place all the same. The poller checks
// every step of the walk against the page before it, so nothing is skipped.
func TestAnIssueThatLeavesTheListingDuringAWalkSkipsNoIssue(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	alice := f.person("alice")
	f.fake.HoldClock(true)
	for i := range 250 {
		if _, err := f.fake.OpenIssue(testRepo, alice, fmt.Sprintf("bulk %d", i), "", github.LabelReady); err != nil {
			t.Fatal(err)
		}
	}
	f.fake.HoldClock(false)
	p := github.NewPoller(f.client, repoRef, time.Time{}, github.LabelReady).LimitPages(1)
	seen := pollUntilDone(t, p, 10, func(poll int) {
		// By the second poll the walk read the burst's first page; an
		// issue on it leaves.
		if poll == 2 {
			if err := f.fake.RemoveLabel(testRepo, 7, f.person("bob"), github.LabelReady); err != nil {
				t.Fatal(err)
			}
		}
	})
	for n := 1; n <= 250; n++ {
		if !seen[n] {
			t.Errorf("issue %d was never reported", n)
		}
	}
}

// Issues of a one-second burst that keep changing while it is walked by
// page do not send the walk back to the burst's first page: every poll gets
// a page further, and the burst costs a bounded number of requests.
func TestChangesDuringAWalkThroughOneSecondDoNotHoldItBack(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	burst(t, f, 250)
	p := github.NewPoller(f.client, repoRef, time.Time{}).LimitPages(1)
	before := len(f.fake.Requests())
	polls := 0
	seen := pollUntilDone(t, p, 8, func(poll int) {
		polls = poll
		// Every poll, another issue already read changes.
		if err := f.fake.AddLabels(testRepo, poll, f.person("bob"), github.LabelReady); err != nil {
			t.Fatal(err)
		}
	})
	if len(seen) != 251 {
		t.Fatalf("the polls found %d of 251 issues", len(seen))
	}
	if requests := len(f.fake.Requests()) - before; requests > 4*(polls+1) {
		t.Fatalf("the walk took %d requests over %d polls", requests, polls+1)
	}
}

// failingSource fails the listing requests whose numbers, counted from 1,
// are in fail.
type failingSource struct {
	github.IssueSource
	calls int
	fail  map[int]bool
}

func (s *failingSource) PollIssues(ctx context.Context, repo github.RepoRef, opts github.ListIssuesOptions, etag string) (github.IssuePage, error) {
	s.calls++
	if s.fail[s.calls] {
		return github.IssuePage{}, core.NewError(core.CodeUnavailable, "GitHub cannot be reached")
	}
	return s.IssueSource.PollIssues(ctx, repo, opts, etag)
}

// A step of a walk is checked against the walk as last acknowledged: a
// poll that was not stored, stored only in part, or whose check could not
// be read moves the walk no further than the position, and an issue that
// leaves a page read meanwhile skips nothing.
func TestAWalkMovesOnlyWithWhatWasAcknowledged(t *testing.T) {
	t.Parallel()
	for name, poll := range map[string]func(t *testing.T, p *github.Poller, src *failingSource) (stored []github.Issue){
		"a poll not stored": func(t *testing.T, p *github.Poller, _ *failingSource) []github.Issue {
			if _, err := p.Poll(bg); err != nil {
				t.Fatal(err)
			}
			return nil
		},
		"a poll stored in part": func(t *testing.T, p *github.Poller, _ *failingSource) []github.Issue {
			c, err := p.Poll(bg)
			if err != nil || len(c.Issues) < 2 {
				t.Fatalf("%d issues, %v", len(c.Issues), err)
			}
			p.Ack(c, len(c.Issues)/2)
			return c.Issues[:len(c.Issues)/2]
		},
		"a check that could not be read": func(t *testing.T, p *github.Poller, src *failingSource) []github.Issue {
			src.fail = map[int]bool{src.calls + 2: true}
			c, err := p.Poll(bg)
			if err == nil || len(c.Issues) != 0 {
				t.Fatalf("a step whose check failed = %d issues, %v", len(c.Issues), err)
			}
			p.Ack(c, len(c.Issues))
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFactory(t)
			alice := f.person("alice")
			f.fake.HoldClock(true)
			for i := range 250 {
				if _, err := f.fake.OpenIssue(testRepo, alice, fmt.Sprintf("bulk %d", i), "", github.LabelReady); err != nil {
					t.Fatal(err)
				}
			}
			f.fake.HoldClock(false)
			src := &failingSource{IssueSource: f.client}
			p := github.NewPoller(src, repoRef, time.Time{}, github.LabelReady).LimitPages(1)
			// The first poll reads from the start, the second the burst's
			// first page.
			for range 2 {
				if _, err := pollAll(p); err != nil {
					t.Fatal(err)
				}
			}
			stored := poll(t, p, src)
			// Issues on the pages read leave the listing.
			for _, n := range []int{3, 150} {
				if err := f.fake.RemoveLabel(testRepo, n, f.person("bob"), github.LabelReady); err != nil {
					t.Fatal(err)
				}
			}
			seen := pollUntilDone(t, p, 10, func(int) {})
			for _, is := range stored {
				seen[is.Ref.Number] = true
			}
			// The burst's first page was stored before; every issue after
			// it that is still listed is reported.
			for n := 101; n <= 250; n++ {
				if n != 150 && !seen[n] {
					t.Errorf("issue %d was never reported", n)
				}
			}
		})
	}
}

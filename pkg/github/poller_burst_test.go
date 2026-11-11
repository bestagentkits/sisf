package github_test

import (
	"fmt"
	"testing"
	"time"

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

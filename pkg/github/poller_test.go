package github_test

import (
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

func TestPollerReportsOnlyWhatChangedAndAnIdleRepositoryCostsANotModified(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	alice := f.person("alice")
	mustOpen(t, f, alice, "one")
	mustOpen(t, f, alice, "two")
	p := github.NewPoller(f.client, repoRef, time.Time{})

	first, err := p.Poll(bg)
	if err != nil || len(first) != 2 {
		t.Fatalf("first poll = %v, %v; a fresh poller reconciles everything", numbers(first), err)
	}
	// GitHub's since is inclusive: the newest issue comes back, and must be
	// recognized as already reported.
	again, err := p.Poll(bg)
	if err != nil || len(again) != 0 {
		t.Fatalf("second poll = %v, %v; nothing changed", numbers(again), err)
	}
	idle, err := p.Poll(bg)
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
	changed, err := p.Poll(bg)
	if err != nil || len(changed) != 1 || changed[0].Ref.Number != 1 || !changed[0].HasLabel(github.LabelReady) {
		t.Fatalf("after a label change: %v, %v", numbers(changed), err)
	}
	if empty, _ := p.Poll(bg); len(empty) != 0 {
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
	if got, err := p.Poll(bg); err == nil || got != nil {
		t.Fatalf("a failed poll = %v, %v; it must report the failure", numbers(got), err)
	}
	if !p.Since().IsZero() {
		t.Fatal("the position moved although nothing was read")
	}
	if got, err := p.Poll(bg); err != nil || len(got) != 1 {
		t.Fatalf("the retry lost the change: %v, %v", numbers(got), err)
	}
	if p.Since().IsZero() {
		t.Fatal("the position must advance to the newest update seen")
	}
}

func TestPollerResumesFromAPersistedPositionAndHonoursLabels(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	alice := f.person("alice")
	mustOpen(t, f, alice, "old", github.LabelReady)
	p := github.NewPoller(f.client, repoRef, time.Time{}, github.LabelReady)
	if got, _ := p.Poll(bg); len(got) != 1 {
		t.Fatalf("labelled issues = %v", numbers(got))
	}
	mustOpen(t, f, alice, "unlabelled")
	mustOpen(t, f, alice, "new", github.LabelReady)

	restarted := github.NewPoller(f.client, repoRef, p.Since(), github.LabelReady)
	got, err := restarted.Poll(bg)
	if err != nil || len(got) == 0 || got[len(got)-1].Ref.Number != 3 {
		t.Fatalf("after a restart: %v, %v", numbers(got), err)
	}
	for _, is := range got {
		if !is.HasLabel(github.LabelReady) {
			t.Fatalf("issue %d does not carry the label the poller filters on", is.Ref.Number)
		}
	}
}

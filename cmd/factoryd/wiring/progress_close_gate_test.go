package wiring

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// commentGitHub is one issue on GitHub as the lifecycle projection edits
// it: its comments, its labels and whether it is closed. While fault is
// set, every read or write of its comments fails with it.
type commentGitHub struct {
	GitHub
	mu       sync.Mutex
	fault    error
	comments []github.Comment
	tries    int      // progress writes tried: each starts by listing the comments
	events   []string // "comment <state>" and "close", in order
	closed   bool
}

var factoryBot = github.User{ID: 1, Login: "factory[bot]"}

func (g *commentGitHub) fail(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fault = err
}

func (g *commentGitHub) GetIssue(_ context.Context, ref core.IssueRef) (github.Issue, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := github.IssueOpen
	if g.closed {
		state = github.IssueClosed
	}
	return github.Issue{Ref: ref, State: state}, nil
}

func (g *commentGitHub) ListComments(context.Context, core.IssueRef, time.Time) ([]github.Comment, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tries++
	if g.fault != nil {
		return nil, g.fault
	}
	return append([]github.Comment(nil), g.comments...), nil
}

func (g *commentGitHub) CreateComment(_ context.Context, ref core.IssueRef, body string) (github.Comment, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fault != nil {
		return github.Comment{}, g.fault
	}
	c := github.Comment{ID: int64(len(g.comments) + 1), Issue: ref, Body: body, Author: factoryBot}
	g.comments = append(g.comments, c)
	g.events = append(g.events, "comment "+nowShown(body))
	return c, nil
}

func (g *commentGitHub) UpdateComment(_ context.Context, _ github.RepoRef, id int64, body string) (github.Comment, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fault != nil {
		return github.Comment{}, g.fault
	}
	for i := range g.comments {
		if g.comments[i].ID == id {
			g.comments[i].Body = body
			g.events = append(g.events, "comment "+nowShown(body))
			return g.comments[i], nil
		}
	}
	return github.Comment{}, core.NewError(core.CodeNotFound, "no such comment")
}

func (g *commentGitHub) AddLabels(context.Context, core.IssueRef, ...string) error { return nil }
func (g *commentGitHub) RemoveLabel(context.Context, core.IssueRef, string) error  { return nil }

func (g *commentGitHub) SetIssueState(_ context.Context, _ core.IssueRef, s github.IssueState) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s == github.IssueClosed {
		g.closed = true
		g.events = append(g.events, "close")
	}
	return nil
}

func (g *commentGitHub) state() (closed bool, tries int, events []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closed, g.tries, append([]string(nil), g.events...)
}

// nowShown is the state a progress comment says the run is in.
func nowShown(body string) string {
	_, rest, _ := strings.Cut(body, "**Now:** `")
	state, _, _ := strings.Cut(rest, "`")
	return state
}

func closeGateControl(t *testing.T) (*Control, *commentGitHub, *sqlstoretest.Clock) {
	t.Helper()
	store, err := openTestStore(t, filepath.Join(t.TempDir(), "factory.db"), core.ZoneControl)
	if err != nil {
		t.Fatal(err)
	}
	clock := sqlstoretest.NewClock(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	gh := &commentGitHub{}
	c := &Control{cfg: ControlConfig{Store: store, GitHub: gh, Clock: clock}, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		projected: map[core.IssueRef]int64{}, progressShown: map[core.IssueRef]string{},
		progress: &github.ProgressComment{Issues: gh, Self: github.Identity{ID: factoryBot.ID, Login: factoryBot.Login}, Factory: "f1"}}
	return c, gh, clock
}

// A GitHub outage as the run ends never costs the issue its last progress
// line: however long GitHub answers 5xx, rate limits or not at all, the
// issue is not closed, and once GitHub answers again the comment shows the
// end, the merge included, and the issue closes after it.
func TestAnOutageAtTheRunsEndDelaysTheCloseUntilTheEndIsShown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, gh, clock := closeGateControl(t)
	issue := core.IssueRef{Owner: "acme", Repo: "app", Number: 5}
	r := core.StateRecord{IssueID: issue, State: core.StateVerified, RunID: "run-1", Version: 1}
	if err := c.projectIssue(ctx, r); err != nil {
		t.Fatal(err)
	}
	outages := []error{
		core.NewError(core.CodeInternal, "GitHub answered 502"),
		core.NewError(core.CodeRateLimited, "GitHub rate limit"),
		core.NewError(core.CodeUnavailable, "connection refused"),
		core.NewError(core.CodeTimeout, "deadline exceeded"),
	}
	merged := handoff.CandidatePR{IssueID: issue, RunID: r.RunID, Attempt: 1, Branch: "sisf/f1/run-1-1", Base: "main", Number: 9,
		Commit: "1111111111111111111111111111111111111111", State: handoff.LandingMerged}
	if err := c.cfg.Store.Update(ctx, func(ctx context.Context, tx state.Tx) error { return handoff.PutCandidatePR(ctx, tx, merged) }); err != nil {
		t.Fatal(err)
	}
	r.State, r.Version = core.StateClosed, 2
	for i := range 3 * maxProgressFailures {
		gh.fail(outages[i%len(outages)])
		if err := c.projectIssue(ctx, r); err == nil {
			t.Fatalf("try %d: the outage went unnoticed", i+1)
		}
		if closed, _, _ := gh.state(); closed || c.progressGivenUp(ctx, issue) {
			t.Fatalf("try %d: the issue closed (%v) or its last progress was given up during an outage", i+1, closed)
		}
		clock.Advance(time.Hour)
	}
	gh.fail(nil)
	if err := c.projectIssue(ctx, r); err != nil {
		t.Fatal(err)
	}
	closed, _, events := gh.state()
	if want := []string{"comment Verified", "comment Closed", "close"}; !closed || strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("GitHub saw %v, want %v", events, want)
	}
	if comments, _ := gh.ListComments(ctx, issue, time.Time{}); len(comments) != 1 || !strings.Contains(comments[0].Body, "**Merged** #9 into `main`") {
		t.Fatalf("the last progress does not say the change is merged: %+v", comments)
	}
}

// A comment GitHub refuses for good, on a locked issue say, is given up
// after maxProgressFailures refusals at the run's end, and only then does
// the issue close. Refusals while the run was in flight do not spend the
// end's tries, and the end is tried at once, not after the pause they
// left.
func TestALockedIssueClosesOnlyAfterTheEndsOwnTriesAreSpent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, gh, clock := closeGateControl(t)
	issue := core.IssueRef{Owner: "acme", Repo: "app", Number: 6}
	r := core.StateRecord{IssueID: issue, State: core.StateDeveloping, RunID: "run-1", Version: 1}
	if err := c.projectIssue(ctx, r); err != nil {
		t.Fatal(err)
	}
	gh.fail(core.NewError(core.CodePermissionDenied, "Unable to create comment because issue is locked."))
	for range maxProgressFailures - 1 {
		r.Version++
		r.State = core.StateMonitoring
		if err := c.projectIssue(ctx, r); err == nil {
			t.Fatal("a refused write went unnoticed")
		}
		clock.Advance(progressPause(maxProgressFailures))
	}
	r.State, r.Version = core.StateClosed, r.Version+1
	for try := 1; try <= maxProgressFailures; try++ {
		_, before, _ := gh.state()
		_ = c.projectIssue(ctx, r)
		closed, after, _ := gh.state()
		if after == before {
			t.Fatalf("end try %d: no write was tried", try)
		}
		if closed != (try == maxProgressFailures) {
			t.Fatalf("end try %d: closed = %v", try, closed)
		}
		clock.Advance(time.Hour)
	}
}

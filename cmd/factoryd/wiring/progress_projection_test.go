package wiring

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// lockedGitHub refuses every read of an issue, as GitHub does for a
// comment on an issue the factory may no longer write; it counts them.
type lockedGitHub struct {
	GitHub
	reads atomic.Int32
}

func (g *lockedGitHub) GetIssue(context.Context, core.IssueRef) (github.Issue, error) {
	g.reads.Add(1)
	return github.Issue{}, core.NewError(core.CodePermissionDenied, "the issue is locked")
}

func progressControl(t *testing.T) (*Control, *lockedGitHub, *sqlstoretest.Clock) {
	t.Helper()
	store, err := openTestStore(t, filepath.Join(t.TempDir(), "factory.db"), core.ZoneControl)
	if err != nil {
		t.Fatal(err)
	}
	clock := sqlstoretest.NewClock(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	gh := &lockedGitHub{}
	c := &Control{cfg: ControlConfig{Store: store, GitHub: gh, Clock: clock}, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		progress:      &github.ProgressComment{Self: github.Identity{ID: 1, Login: "factory[bot]"}},
		progressShown: map[core.IssueRef]string{}}
	return c, gh, clock
}

// An issue whose run ended before it ever had a progress comment, as one
// closed before the comment existed, is never given one.
func TestAnIssueThatEndedWithoutAProgressCommentIsNotGivenOne(t *testing.T) {
	t.Parallel()
	c, gh, _ := progressControl(t)
	for _, s := range []core.State{core.StateClosed, core.StateRolledBack, core.StateBlocked} {
		r := core.StateRecord{IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 3}, State: s, RunID: "run-old"}
		if err := c.showProgress(context.Background(), r); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if gh.reads.Load() != 0 {
		t.Fatalf("GitHub was asked %d times about an issue that ended long ago", gh.reads.Load())
	}
}

// A progress comment that cannot be written is tried again only after a
// pause that grows; once its run ended and it failed maxProgressFailures
// times it is given up, so the issue's close is not held back by it.
func TestAProgressCommentThatCannotBeWrittenBacksOffAndIsGivenUpAtTheEnd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, gh, clock := progressControl(t)
	issue := core.IssueRef{Owner: "acme", Repo: "app", Number: 4}
	r := core.StateRecord{IssueID: issue, State: core.StateDeveloping, RunID: "run-1"}
	if err := c.showProgress(ctx, r); err == nil || gh.reads.Load() != 1 {
		t.Fatalf("the first write: %v after %d reads", err, gh.reads.Load())
	}
	if err := c.showProgress(ctx, r); err == nil || gh.reads.Load() != 1 {
		t.Fatalf("tried again at once: %v after %d reads", err, gh.reads.Load())
	}
	clock.Advance(progressPause(1))
	if err := c.showProgress(ctx, r); err == nil || gh.reads.Load() != 2 {
		t.Fatalf("not tried again after the pause: %v after %d reads", err, gh.reads.Load())
	}
	r.State = core.StateClosed
	for range maxProgressFailures {
		clock.Advance(time.Hour)
		_ = c.showProgress(ctx, r)
		if gh.reads.Load() > 1+maxProgressFailures {
			break
		}
	}
	if !c.progressGivenUp(ctx, issue) {
		t.Fatal("the ended issue's progress was not given up")
	}
	reads := gh.reads.Load()
	clock.Advance(time.Hour)
	if err := c.showProgress(ctx, r); err != nil || gh.reads.Load() != reads {
		t.Fatalf("a given-up issue was tried again: %v", err)
	}
}

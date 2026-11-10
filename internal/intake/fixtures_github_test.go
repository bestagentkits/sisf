package intake_test

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// fakeGitHub is the issue side of GitHub, kept in memory. It records every
// call, so a test can show that nothing was read or written.
type fakeGitHub struct {
	mu       sync.Mutex
	now      func() time.Time
	issues   map[core.IssueRef]github.Issue
	comments []github.Comment
	reads    int
	down     bool
}

func (g *fakeGitHub) GetIssue(_ context.Context, ref core.IssueRef) (github.Issue, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reads++
	issue, ok := g.issues[ref]
	switch {
	case g.down:
		return github.Issue{}, core.NewError(core.CodeUnavailable, "GitHub cannot be reached")
	case !ok:
		return github.Issue{}, core.NewError(core.CodeNotFound, "no such issue")
	}
	issue.Labels = slices.Clone(issue.Labels)
	return issue, nil
}

func (g *fakeGitHub) ListComments(_ context.Context, ref core.IssueRef, _ time.Time) ([]github.Comment, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reads++
	if g.down {
		return nil, core.NewError(core.CodeUnavailable, "GitHub cannot be reached")
	}
	var out []github.Comment
	for _, c := range g.comments {
		if c.Issue == ref {
			out = append(out, c)
		}
	}
	return out, nil
}

func (g *fakeGitHub) CreateComment(_ context.Context, ref core.IssueRef, body string) (github.Comment, error) {
	if g.isDown() {
		return github.Comment{}, core.NewError(core.CodeUnavailable, "GitHub cannot be reached")
	}
	return g.comment(ref, factoryAccount, body), nil
}

func (g *fakeGitHub) AddLabels(_ context.Context, ref core.IssueRef, names ...string) error {
	if g.isDown() {
		return core.NewError(core.CodeUnavailable, "GitHub cannot be reached")
	}
	g.label(ref, names...)
	return nil
}

func (g *fakeGitHub) RemoveLabel(_ context.Context, ref core.IssueRef, name string) error {
	g.unlabel(ref, name)
	return nil
}

func (g *fakeGitHub) isDown() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.down
}

// comment adds a comment by who and returns it.
func (g *fakeGitHub) comment(ref core.IssueRef, who person, body string) github.Comment {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := github.Comment{ID: int64(len(g.comments) + 1), Issue: ref, Body: body, Author: who.user,
		AuthorAssociation: who.association, CreatedAt: g.now(), UpdatedAt: g.now()}
	g.comments = append(g.comments, c)
	return c
}

func (g *fakeGitHub) label(ref core.IssueRef, names ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	issue := g.issues[ref]
	for _, name := range names {
		if !issue.HasLabel(name) {
			issue.Labels = append(issue.Labels, github.Label{Name: name})
		}
	}
	g.issues[ref] = issue
}

func (g *fakeGitHub) unlabel(ref core.IssueRef, name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	issue := g.issues[ref]
	issue.Labels = slices.DeleteFunc(slices.Clone(issue.Labels), func(l github.Label) bool { return l.Name == name })
	g.issues[ref] = issue
}

// edit changes the stored issue.
func (g *fakeGitHub) edit(ref core.IssueRef, change func(*github.Issue)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	issue := g.issues[ref]
	change(&issue)
	g.issues[ref] = issue
}

// factoryComments returns the comments the factory posted on ref.
func (g *fakeGitHub) factoryComments(ref core.IssueRef) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, c := range g.comments {
		if c.Issue == ref && intake.IsFactoryComment(c.Body) {
			out = append(out, c.Body)
		}
	}
	return out
}

func (g *fakeGitHub) readCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reads
}

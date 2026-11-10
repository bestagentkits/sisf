package github

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// ProgressComment keeps one status comment per issue, updated in place, so an
// issue shows the current state and not a log of every step.
//
// The comment is found by a hidden marker, not remembered, so it survives a
// restart. The marker alone proves nothing: anyone can paste it into their
// own comment. The comment is trusted as ours only when the factory's account
// wrote it.
type ProgressComment struct {
	Issues Issues
	// Self is the factory's own account.
	Self Identity
	// Factory is named in the marker, so two factories on the same
	// repository keep separate comments.
	Factory core.FactoryID

	mu sync.Mutex
}

// Marker is the hidden line that identifies this factory's progress comment.
func (p *ProgressComment) Marker() string {
	return "<!-- sisf:progress factory=" + string(p.Factory) + " -->"
}

// Upsert makes the issue carry exactly one progress comment with body. It
// creates the comment the first time, edits it afterwards, and makes no call
// when nothing changed.
func (p *ProgressComment) Upsert(ctx context.Context, ref core.IssueRef, body string) (Comment, error) {
	if err := p.Factory.Validate(); err != nil {
		return Comment{}, err
	}
	if p.Self.IsZero() || p.Issues == nil {
		return Comment{}, core.NewError(core.CodeInvalidArgument, "a progress comment needs the factory's account and an issue client")
	}
	if strings.Contains(body, "<!-- sisf:progress") {
		return Comment{}, core.NewError(core.CodeInvalidArgument, "progress text must not contain a progress marker")
	}
	full := p.Marker() + "\n" + body
	p.mu.Lock()
	defer p.mu.Unlock()
	comments, err := p.Issues.ListComments(ctx, ref, time.Time{})
	if err != nil {
		return Comment{}, err
	}
	if existing, ok := p.find(comments); ok {
		if existing.Body == full {
			return existing, nil
		}
		updated, err := p.Issues.UpdateComment(ctx, RepoRef{Owner: ref.Owner, Name: ref.Repo}, existing.ID, full)
		updated.Issue = ref
		return updated, err
	}
	return p.Issues.CreateComment(ctx, ref, full)
}

// find returns the oldest comment that carries the marker and was written by
// the factory.
func (p *ProgressComment) find(comments []Comment) (Comment, bool) {
	var found Comment
	for _, c := range comments {
		if !p.Self.Matches(c.Author) || !strings.HasPrefix(c.Body, p.Marker()) {
			continue
		}
		if found.ID == 0 || c.ID < found.ID {
			found = c
		}
	}
	return found, found.ID != 0
}

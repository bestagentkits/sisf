package wiring

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// editIntent is what one edit of the factory changes on an issue besides
// its comments. The zero value posts comments only.
type editIntent struct {
	add, remove []string
	close       bool
}

// explains reports whether after differs from before by nothing but what
// the edit intended, comments aside: the same title and body, the labels of
// before with the edit's added and removed, and the state the edit leaves.
// Anything more was done by someone else in the meantime.
func (e editIntent) explains(before, after github.Issue) bool {
	if before.Title != after.Title || before.Body != after.Body {
		return false
	}
	state := before.State
	if e.close {
		state = github.IssueClosed
	}
	if after.State != state {
		return false
	}
	want := map[string]bool{}
	for _, l := range before.Labels {
		want[l.Name] = true
	}
	for _, name := range e.add {
		want[name] = true
	}
	for _, name := range e.remove {
		delete(want, name)
	}
	got := map[string]bool{}
	for _, l := range after.Labels {
		got[l.Name] = true
	}
	if len(got) != len(want) {
		return false
	}
	for name := range want {
		if !got[name] {
			return false
		}
	}
	return true
}

// editIssue performs one edit of the factory on an issue and records it,
// so reconciliation does not bring the factory's own edit back as a change
// for intake (see control.RecordFactoryWrite). The issue is read just
// before and just after the edit; a failed read before it fails the edit,
// since GitHub is then unlikely to take the edit either. The edit is
// recorded as the factory's own only when the two reads differ by nothing
// but what intent says it does. When the edit cannot be recorded it still
// stands: the next poll then finds it, and intake reads the issue once more
// and finds nothing new.
func (c *Control) editIssue(ctx context.Context, ref core.IssueRef, intent editIntent, edit func(ctx context.Context) error) error {
	if ref.Validate() != nil {
		return edit(ctx)
	}
	before, err := c.cfg.GitHub.GetIssue(ctx, ref)
	if err != nil {
		return err
	}
	if err := edit(ctx); err != nil {
		return err
	}
	after, err := c.cfg.GitHub.GetIssue(ctx, ref)
	if err == nil {
		err = control.RecordFactoryWrite(ctx, c.cfg.Store, before, after, intent.explains(before, after))
	}
	if err != nil {
		c.log.Warn("the factory's edit of an issue could not be recorded; the next poll hands it to intake once more",
			"issue", ref.String(), "code", string(core.CodeOf(err)))
	}
	return nil
}

// confirmOwnEdit lists the comments of the issue updated at or after since
// that do not carry the factory's marker (see control.ConfirmOwnEdit).
func (c *Control) confirmOwnEdit(ctx context.Context, ref core.IssueRef, since time.Time) ([]github.Comment, error) {
	comments, err := c.cfg.GitHub.ListComments(ctx, ref, since)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(comments, func(cm github.Comment) bool { return intake.IsFactoryComment(cm.Body) }), nil
}

// outboxIssue is the issue an outbox message edits: intake's effects name
// it as "issue", the supervisor's notices as "issue_id". The zero reference
// when the message names none it can read.
func outboxIssue(m state.OutboxMessage) core.IssueRef {
	var named struct {
		Issue   core.IssueRef `json:"issue"`
		IssueID core.IssueRef `json:"issue_id"`
	}
	if json.Unmarshal(m.Payload, &named) != nil {
		return core.IssueRef{}
	}
	if named.Issue.Validate() == nil {
		return named.Issue
	}
	return named.IssueID
}

// outboxIntent is what an outbox message changes on its issue besides
// comments: the labels of intake's label effects.
func outboxIntent(m state.OutboxMessage) editIntent {
	if m.Topic != intake.TopicLabels {
		return editIntent{}
	}
	var labels struct {
		Add    []string `json:"add_labels"`
		Remove []string `json:"remove_labels"`
	}
	if json.Unmarshal(m.Payload, &labels) != nil {
		return editIntent{}
	}
	return editIntent{add: labels.Add, remove: labels.Remove}
}

package wiring

import (
	"context"
	"encoding/json"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// editIssue performs one edit of the factory on an issue and records it,
// so reconciliation does not bring the factory's own edit back as a change
// for intake (see control.RecordFactoryWrite). The issue is read just
// before and just after the edit; a failed read before it fails the edit,
// since GitHub is then unlikely to take the edit either. When the edit
// cannot be recorded it still stands: the next poll then finds it, and
// intake reads the issue once more and finds nothing new.
func (c *Control) editIssue(ctx context.Context, ref core.IssueRef, edit func(ctx context.Context) error) error {
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
		err = control.RecordFactoryWrite(ctx, c.cfg.Store, ref, before.UpdatedAt, after.UpdatedAt)
	}
	if err != nil {
		c.log.Warn("the factory's edit of an issue could not be recorded; the next poll hands it to intake once more",
			"issue", ref.String(), "code", string(core.CodeOf(err)))
	}
	return nil
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

package control

import (
	"context"
	"encoding/json"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

const (
	// issueViewNamespace holds what the dashboard shows about an issue that
	// the lifecycle record does not carry. It is a projection of webhooks and
	// holds data only; nothing reads it as an instruction.
	issueViewNamespace = "control.issue_view"

	maxProjectedComments = 20
	maxCommentRunes      = 4000
	maxTitleRunes        = 500
	maxLabels            = 50
)

// projectedComment is one comment kept for display.
type projectedComment struct {
	ID        int64     `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// issueProjection is the stored document of one issue. Every string in it
// came from GitHub and is untrusted.
type issueProjection struct {
	Title       string             `json:"title"`
	Author      string             `json:"author"`
	GitHubState string             `json:"github_state"`
	Labels      []string           `json:"labels,omitempty"`
	IssueAt     time.Time          `json:"issue_updated_at"`
	Comments    []projectedComment `json:"comments,omitempty"`
}

// readProjection returns the stored document of an issue and its version,
// or a zero document at version 0 when none exists yet.
func readProjection(ctx context.Context, tx state.KV, ref core.IssueRef) (issueProjection, int64, error) {
	entry, err := tx.GetKV(ctx, issueViewNamespace, ref.String())
	if core.HasCode(err, core.CodeNotFound) {
		return issueProjection{}, 0, nil
	}
	if err != nil {
		return issueProjection{}, 0, err
	}
	var p issueProjection
	if err := json.Unmarshal(entry.Value, &p); err != nil {
		// A document this package wrote and cannot read again is damage,
		// not a missing page; say so instead of showing an empty issue.
		return issueProjection{}, 0, core.NewError(core.CodeInternal, "the stored issue view is unreadable").WithCause(err)
	}
	return p, entry.Version, nil
}

// projectEvent folds a webhook into the stored view of its issue. A delivery
// that GitHub produced earlier than what is already stored is ignored for the
// issue fields, because deliveries can arrive out of order.
func projectEvent(ctx context.Context, tx state.KV, ev github.WebhookEvent) error {
	if ev.Issue == nil || ev.Issue.Ref.Validate() != nil {
		return nil
	}
	ref := ev.Issue.Ref
	current, version, err := readProjection(ctx, tx, ref)
	if err != nil {
		return err
	}
	next := current
	if ev.Issue.UpdatedAt.IsZero() || !ev.Issue.UpdatedAt.Before(current.IssueAt) {
		next.Title = clip(ev.Issue.Title, maxTitleRunes)
		next.Author = ev.Issue.Author.Login
		next.GitHubState = string(ev.Issue.State)
		next.IssueAt = ev.Issue.UpdatedAt
		next.Labels = next.Labels[:0:0]
		for _, l := range ev.Issue.Labels {
			if len(next.Labels) < maxLabels {
				next.Labels = append(next.Labels, clip(l.Name, maxTitleRunes))
			}
		}
	}
	if ev.Comment != nil {
		next.Comments = applyComment(next.Comments, ev.Action, *ev.Comment)
	}
	value, err := json.Marshal(next)
	if err != nil {
		return core.NewError(core.CodeInternal, "the issue view could not be encoded").WithCause(err)
	}
	_, err = tx.PutKV(ctx, state.KVEntry{Namespace: issueViewNamespace, Key: ref.String(), Value: value, Version: version})
	return err
}

// applyComment adds, replaces or removes one comment and keeps the newest few.
func applyComment(comments []projectedComment, action string, c github.Comment) []projectedComment {
	out := slices.DeleteFunc(slices.Clone(comments), func(p projectedComment) bool { return p.ID == c.ID })
	if action == "deleted" {
		return out
	}
	out = append(out, projectedComment{ID: c.ID, Author: c.Author.Login, Body: clip(c.Body, maxCommentRunes), CreatedAt: c.CreatedAt})
	slices.SortStableFunc(out, func(a, b projectedComment) int { return a.CreatedAt.Compare(b.CreatedAt) })
	if len(out) > maxProjectedComments {
		out = out[len(out)-maxProjectedComments:]
	}
	return out
}

// clip bounds untrusted text by characters, never cutting inside one.
func clip(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

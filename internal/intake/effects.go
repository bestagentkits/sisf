package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Outbox topics of intake. A message is written in the transaction that
// decided it and delivered afterwards, so a decision and what GitHub shows
// of it cannot drift apart.
const (
	// TopicComment posts one comment on an issue.
	TopicComment = "intake.comment"
	// TopicLabels adds and removes labels on an issue.
	TopicLabels = "intake.labels"
)

// effect is the payload of an outbox message of intake.
type effect struct {
	Issue   core.IssueRef `json:"issue"`
	Comment string        `json:"comment,omitempty"`
	// Token is a random value that goes into the hidden marker of the
	// comment. Nobody can post that marker before the comment exists, so
	// nobody can make the factory believe it already said something.
	Token        string   `json:"token,omitempty"`
	AddLabels    []string `json:"add_labels,omitempty"`
	RemoveLabels []string `json:"remove_labels,omitempty"`
}

// markerPrefix opens the hidden marker every comment of intake ends with.
const markerPrefix = "<!-- sisf:intake "

// marker returns the hidden marker of the comment queued under key.
func marker(key, token string) string { return markerPrefix + key + " " + token + " -->" }

// IsFactoryComment reports whether a comment body carries the marker of an
// intake comment. Such a comment is never read as an answer, whichever
// account posted it.
func IsFactoryComment(body string) bool { return strings.Contains(body, markerPrefix) }

// commentEffect queues a comment. name must be unique per issue: it becomes
// the outbox key, so the same comment is never queued twice.
func commentEffect(issue core.IssueRef, name, body string) (state.OutboxMessage, error) {
	token, err := approvals.NewNonce()
	if err != nil {
		return state.OutboxMessage{}, err
	}
	return message(TopicComment, issue, name, effect{Issue: issue, Comment: body, Token: token})
}

// labelEffect queues label changes.
func labelEffect(issue core.IssueRef, name string, add, remove []string) (state.OutboxMessage, error) {
	return message(TopicLabels, issue, name, effect{Issue: issue, AddLabels: add, RemoveLabels: remove})
}

func message(topic string, issue core.IssueRef, name string, e effect) (state.OutboxMessage, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return state.OutboxMessage{}, core.NewError(core.CodeInternal, "an intake effect cannot be encoded").WithCause(err)
	}
	return state.OutboxMessage{Topic: topic, Key: "intake/" + issue.String() + "/" + name, Payload: raw}, nil
}

// IssueWriter is the part of the GitHub client Deliver needs.
type IssueWriter interface {
	ListComments(ctx context.Context, ref core.IssueRef, since time.Time) ([]github.Comment, error)
	CreateComment(ctx context.Context, ref core.IssueRef, body string) (github.Comment, error)
	AddLabels(ctx context.Context, ref core.IssueRef, names ...string) error
	RemoveLabel(ctx context.Context, ref core.IssueRef, name string) error
}

// Deliver performs one outbox message of intake on GitHub. The caller marks
// the message sent when Deliver returns nil and schedules a retry otherwise.
//
// Delivery is safe to repeat. A comment ends with a hidden marker naming its
// message; when the issue already shows that marker the comment is not
// posted again. Adding a label that is there, or removing one that is not,
// changes nothing. A message of another topic is NOT_SUPPORTED.
func Deliver(ctx context.Context, issues IssueWriter, m state.OutboxMessage) error {
	if m.Topic != TopicComment && m.Topic != TopicLabels {
		return core.NewError(core.CodeNotSupported, fmt.Sprintf("outbox topic %q does not belong to intake", m.Topic))
	}
	var e effect
	if err := json.Unmarshal(m.Payload, &e); err != nil {
		return core.NewError(core.CodeInvalidArgument, "the outbox message of intake cannot be read").WithCause(err)
	}
	if err := e.Issue.Validate(); err != nil {
		return err
	}
	if m.Topic == TopicLabels {
		return deliverLabels(ctx, issues, e)
	}
	if e.Token == "" {
		return core.NewError(core.CodeInvalidArgument, "the comment of intake carries no marker token")
	}
	mark := marker(m.Key, e.Token)
	posted, err := issues.ListComments(ctx, e.Issue, time.Time{})
	if err != nil {
		return err
	}
	for _, c := range posted {
		if strings.Contains(c.Body, mark) {
			return nil
		}
	}
	_, err = issues.CreateComment(ctx, e.Issue, e.Comment+"\n\n"+mark)
	return err
}

func deliverLabels(ctx context.Context, issues IssueWriter, e effect) error {
	if len(e.AddLabels) > 0 {
		if err := issues.AddLabels(ctx, e.Issue, e.AddLabels...); err != nil {
			return err
		}
	}
	for _, name := range e.RemoveLabels {
		// A label that is already gone is the wanted result.
		if err := issues.RemoveLabel(ctx, e.Issue, name); err != nil && !core.HasCode(err, core.CodeNotFound) {
			return err
		}
	}
	return nil
}

// questions maps each gap to the question asked about it.
var questions = map[Gap]string{
	GapKind:    "What kind of request is this? A maintainer adds one of the labels `kind:bug`, `kind:improvement` or `kind:feature`.",
	GapScope:   "What should change? Describe the current and the expected behavior under a `### Scope` heading.",
	GapDoD:     "How will we know it is done? List measurable criteria under a `### Definition of Done` heading, one per line starting with `- `.",
	GapFeature: "Which existing feature does this improve? Name it under a `### Affected features` heading, spelled as in the feature catalogue.",
}

// clarifyComment asks for what is missing and says when the wait ends.
func clarifyComment(missing []Gap, deadline time.Time) string {
	var b strings.Builder
	b.WriteString("### More information needed\n\nBefore work on this issue can be scoped, please answer in a comment or by editing the issue:\n\n")
	for _, gap := range missing {
		fmt.Fprintf(&b, "- %s\n", questions[gap])
	}
	fmt.Fprintf(&b, "\nWithout a complete answer by %s the issue is parked as `%s` and nothing more is done on it.",
		deadline.UTC().Format("2006-01-02 15:04 MST"), github.LabelNeedsInfo)
	return b.String()
}

// parkedComment says that the issue stopped waiting and how to resume it.
func parkedComment(why string) string {
	return fmt.Sprintf("### Parked: information still missing\n\n%s Nothing more will be asked or done on this issue.\n\n"+
		"To resume, add the missing information and remove the `%s` label.", why, github.LabelNeedsInfo)
}

// sowComment attaches a scope version to the issue. The text taken from the
// issue is shown inside a code fence, where it is inert.
func sowComment(sow SoW) string {
	return fmt.Sprintf("### Scope of Work v%d\n\n- Digest: `%s`\n- Policy revision: `%s`\n\n%s\n\n"+
		"An edit that changes this scope makes a new version; an approval given for this digest does not cover it.",
		sow.Version, sow.Digest, sow.PolicyRevision, fenced(sow.Text()))
}

// approvalComment says that the scope waits for the Owner and why.
func approvalComment(sow SoW, requestID string, c Classification) string {
	return fmt.Sprintf("### Waiting for approval\n\nScope of Work v%d needs the Owner's approval before any work starts: %s\n\n"+
		"Approval request `%s` is in the approval inbox. Comments and labels on this issue do not approve it.",
		sow.Version, riskSummary(c), requestID)
}

// blockedComment says that the approval did not come.
func blockedComment(why string) string {
	return fmt.Sprintf("### Blocked\n\n%s No work will start on this issue.", why)
}

// changesComment passes on that the approver wants the scope changed.
func changesComment(sow SoW) string {
	return fmt.Sprintf("### Changes requested\n\nThe approver asked for changes to Scope of Work v%d. "+
		"Edit the issue; a changed scope becomes a new version with a new approval request.", sow.Version)
}

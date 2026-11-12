package github

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// handledActions lists, per event type, the actions the factory reacts to.
// A verified delivery of any other type or action is acknowledged and
// ignored: GitHub is told it arrived, and nothing is processed.
var handledActions = map[string][]string{
	"issues":        {"opened", "edited", "closed", "reopened", "labeled", "unlabeled"},
	"issue_comment": {"created", "edited", "deleted"},
	"pull_request": {"opened", "edited", "closed", "reopened", "synchronize", "ready_for_review",
		"converted_to_draft", "labeled", "unlabeled"},
}

type webhookPayload struct {
	Action       string `json:"action"`
	Installation *struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository  *wireRepo    `json:"repository"`
	Sender      wireUser     `json:"sender"`
	Issue       *wireIssue   `json:"issue"`
	Comment     *wireComment `json:"comment"`
	PullRequest *wirePull    `json:"pull_request"`
	Label       *wireLabel   `json:"label"`
}

// handles reports whether the factory reacts to this type and action.
func handles(eventType, action string) bool {
	for _, a := range handledActions[eventType] {
		if a == action {
			return true
		}
	}
	return false
}

func handlesType(eventType string) bool { _, ok := handledActions[eventType]; return ok }

// decodePayload parses a verified body into a WebhookEvent. It rejects a
// payload that lacks what its event type needs.
func decodePayload(deliveryID, eventType string, body []byte, now time.Time) (WebhookEvent, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	var p webhookPayload
	if err := dec.Decode(&p); err != nil {
		return WebhookEvent{}, core.NewError(core.CodeInvalidArgument, "the webhook payload is not valid JSON").WithCause(err)
	}
	ev := WebhookEvent{
		DeliveryID: deliveryID, Type: eventType, Action: p.Action, Sender: p.Sender.user(), ReceivedAt: now,
	}
	if p.Installation != nil {
		ev.InstallationID = p.Installation.ID
	}
	if p.Repository != nil {
		ev.Repo = p.Repository.ref()
	}
	if p.Label != nil {
		ev.Label = &Label{Name: p.Label.Name}
	}
	missing := core.NewError(core.CodeInvalidArgument, "the webhook payload lacks the object its event type carries")
	switch eventType {
	case "issues":
		if p.Issue == nil {
			return WebhookEvent{}, missing
		}
		issue := p.Issue.issue(ev.Repo)
		ev.Issue = &issue
	case "issue_comment":
		if p.Issue == nil || p.Comment == nil {
			return WebhookEvent{}, missing
		}
		issue := p.Issue.issue(ev.Repo)
		comment := p.Comment.comment(issue.Ref)
		ev.Issue, ev.Comment = &issue, &comment
	case "pull_request":
		if p.PullRequest == nil {
			return WebhookEvent{}, missing
		}
		pr := p.PullRequest.pull(ev.Repo)
		ev.PullRequest = &pr
	}
	return ev, nil
}

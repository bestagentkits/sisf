package approvalactions

import (
	"context"
	"encoding/json"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Bounds of one inbox listing.
const (
	defaultListLimit = 100
	maxListLimit     = 500
)

// Inbox is the approval store as the inbox uses it; *approvals.KVStore is one.
type Inbox interface {
	List(ctx context.Context, f approvals.Filter) ([]approvals.Request, error)
	Get(ctx context.Context, id string) (approvals.Request, error)
	Decision(ctx context.Context, requestID string) (approvals.Approval, error)
	// Submit records the decision of the authenticated actor.
	Submit(ctx context.Context, actor core.Actor, sub approvals.Submission) (approvals.Request, error)
}

// New returns the inbox actions over inbox.
func New(inbox Inbox) ([]core.Action, error) {
	if inbox == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the approval actions need the approval inbox")
	}
	h := handlers{inbox}
	byName := map[string]core.ActionHandler{ActionList: h.list, ActionGet: h.get, ActionDecide: h.decide}
	specs := Specs()
	out := make([]core.Action, 0, len(specs))
	for _, spec := range specs {
		out = append(out, core.Action{Spec: spec, Handle: byName[spec.Name]})
	}
	return out, nil
}

type handlers struct{ inbox Inbox }

type listInput struct {
	Statuses []approvals.Status `json:"statuses,omitempty"`
	Issue    string             `json:"issue,omitempty"`
	Limit    int                `json:"limit,omitempty"`
}

// RequestList is the answer of approvals.list.
type RequestList struct {
	Requests []approvals.Request `json:"requests"`
}

func (h handlers) list(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	in, err := actions.DecodeInput[listInput](req)
	if err != nil {
		return core.ActionResult{}, err
	}
	filter := approvals.Filter{Statuses: in.Statuses, Limit: in.Limit}
	for _, st := range in.Statuses {
		if !st.Valid() {
			return core.ActionResult{}, core.NewError(core.CodeInvalidArgument, "unknown approval status "+string(st))
		}
	}
	switch {
	case in.Limit == 0:
		filter.Limit = defaultListLimit
	case in.Limit < 0 || in.Limit > maxListLimit:
		return core.ActionResult{}, core.NewError(core.CodeInvalidArgument, "limit must be from 1 to 500")
	}
	if in.Issue != "" {
		if filter.IssueID, err = core.ParseIssueRef(in.Issue); err != nil {
			return core.ActionResult{}, err
		}
	}
	requests, err := h.inbox.List(ctx, filter)
	if err != nil {
		return core.ActionResult{}, err
	}
	if requests == nil {
		requests = []approvals.Request{}
	}
	return actions.JSONResult(RequestList{Requests: requests})
}

// Proposal is the answer of approvals.get: the request and, once decided,
// the decision recorded on it.
type Proposal struct {
	Request  approvals.Request   `json:"request"`
	Decision *approvals.Approval `json:"decision"`
}

func (h handlers) get(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	in, err := actions.DecodeInput[struct {
		ID string `json:"id"`
	}](req)
	if err != nil {
		return core.ActionResult{}, err
	}
	request, err := h.read(ctx, in.ID)
	if err != nil {
		return core.ActionResult{}, err
	}
	out := Proposal{Request: request}
	decision, err := h.inbox.Decision(ctx, request.ID)
	switch {
	case err == nil:
		out.Decision = &decision
	case !core.HasCode(err, core.CodeNotFound):
		return core.ActionResult{}, err
	}
	return actions.JSONResult(out)
}

// read returns one request, with a next action for an unknown id.
func (h handlers) read(ctx context.Context, id string) (approvals.Request, error) {
	if id == "" {
		return approvals.Request{}, core.NewError(core.CodeInvalidArgument, "an approval request id is needed")
	}
	request, err := h.inbox.Get(ctx, id)
	if core.HasCode(err, core.CodeNotFound) {
		return approvals.Request{}, core.NewError(core.CodeNotFound, "no approval request has this id",
			core.NextAction{Action: ActionList, Description: "list the approval requests"})
	}
	return request, err
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

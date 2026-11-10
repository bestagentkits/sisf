package approvalactions

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

type decideInput struct {
	RequestID      string             `json:"request_id"`
	Nonce          string             `json:"nonce"`
	SubjectDigest  core.Digest        `json:"subject_digest"`
	PolicyRevision core.CommitSHA     `json:"policy_revision"`
	Decision       approvals.Decision `json:"decision"`
	Reason         string             `json:"reason,omitempty"`
	Conditions     struct {
		BudgetCap core.Micros `json:"budget_cap,omitempty"`
		ExpiresAt time.Time   `json:"expires_at,omitzero"`
	} `json:"conditions,omitzero"`
}

// Decided is the answer of approvals.decide.
type Decided struct {
	Request approvals.Request `json:"request"`
}

// decide records the actor's decision through the store, which checks the
// role, the step-up, the echoed nonce, subject and policy, and single use.
// A dry run reads the request and reports the status it would take,
// refusing a request that is no longer open or a view that is outdated.
func (h handlers) decide(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	in, err := actions.DecodeInput[decideInput](req)
	if err != nil {
		return core.ActionResult{}, err
	}
	if !in.Decision.Valid() {
		return core.ActionResult{}, core.NewError(core.CodeInvalidArgument, "decision must be approve, reject or request_changes")
	}
	request, err := h.read(ctx, in.RequestID)
	if err != nil {
		return core.ActionResult{}, err
	}
	if req.DryRun {
		reread := core.NextAction{Action: ActionGet, Description: "read the request as it stands now", Params: map[string]string{"id": request.ID}}
		switch {
		case !request.Status.Open():
			return core.ActionResult{}, core.NewError(core.CodeConflict, "the approval request is "+string(request.Status)+" and takes no decision", reread)
		case in.Nonce != request.Nonce || in.SubjectDigest != request.SubjectDigest || in.PolicyRevision != request.PolicyRevision:
			return core.ActionResult{}, core.NewError(core.CodeApprovalRequired, "the decision was made on an outdated view of the request", reread)
		}
		after := map[approvals.Decision]approvals.Status{approvals.DecisionApprove: approvals.StatusApproved,
			approvals.DecisionReject: approvals.StatusRejected, approvals.DecisionRequestChanges: approvals.StatusChangesRequested}[in.Decision]
		return core.ActionResult{Plan: &core.DryRun{Changes: []core.Change{{
			Resource: "approval:" + request.ID, Op: core.ChangeUpdate,
			Before: mustJSON(map[string]approvals.Status{"status": request.Status}),
			After:  mustJSON(map[string]approvals.Status{"status": after}),
		}}, Warnings: []string{request.RiskSummary}}}, nil
	}
	decided, err := h.inbox.Submit(ctx, req.Actor, approvals.Submission{
		RequestID: request.ID, Nonce: in.Nonce, SubjectDigest: in.SubjectDigest, PolicyRevision: in.PolicyRevision,
		Decision: in.Decision, Reason: in.Reason,
		Conditions: approvals.Conditions{BudgetCap: in.Conditions.BudgetCap, ExpiresAt: in.Conditions.ExpiresAt},
	})
	if err != nil {
		return core.ActionResult{}, err
	}
	return actions.JSONResult(Decided{Request: decided})
}

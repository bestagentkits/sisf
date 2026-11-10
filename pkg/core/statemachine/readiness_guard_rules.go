package statemachine

import (
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// checkDelegation requires policy to allow, outright, the pinned scope of
// this issue under a pinned policy revision. A scope that needs the Owner is
// refused here and pointed to the approval request.
func checkDelegation(t core.Transition, p DelegationProof) error {
	switch {
	case p.IssueID != t.IssueID:
		return refuse(GuardDelegation, t, "the scope that was judged belongs to another issue")
	case p.ScopeDigest.Validate() != nil:
		return refuse(GuardDelegation, t, "the scope that was judged is not identified by a full digest")
	case p.PolicyRevision.Validate() != nil:
		return refuse(GuardDelegation, t, "the policy the scope was judged under is not pinned to a commit")
	}
	if !p.Decision.Allowed() {
		refusal := refuse(GuardDelegation, t, "the scope is not within the Owner's delegation and needs an approval")
		refusal.Details["effect"] = string(p.Decision.Effect)
		refusal.Details["allowed_event"] = string(core.EventApprovalRequired)
		if p.Decision.Reason != "" {
			refusal.Details["reason"] = p.Decision.Reason
		}
		return refusal
	}
	return nil
}

// checkApproval requires a complete, signed decision by a person that
// belongs to a request to approve the scope of this issue in this factory,
// was made while the request could still be decided, is approved and not yet
// used, and authorizes the scope and the policy in force at this instant. The
// decision carries a full digest and a full commit, so a scope or policy in
// force that is unknown or unpinned can never equal what was approved.
func checkApproval(t core.Transition, p ApprovalProof, now time.Time) error {
	request, approval := p.Request, p.Approval
	if err := request.Validate(); err != nil {
		return refuse(GuardApproval, t, "the approval request is not valid: %v", err).WithCause(err)
	}
	if err := approval.Validate(); err != nil {
		return refuse(GuardApproval, t, "the approval is not valid: %v", err).WithCause(err)
	}
	switch {
	case approval.Signature.Validate() != nil:
		return refuse(GuardApproval, t, "the approval is not signed")
	case p.FactoryID.Validate() != nil || request.FactoryID != p.FactoryID:
		return refuse(GuardApproval, t, "the approval was requested in another factory")
	case request.IssueID != t.IssueID:
		return refuse(GuardApproval, t, "the approval concerns another issue")
	case request.Action != approvals.ActionApproveScope:
		return refuse(GuardApproval, t, "the approval is for action %q, not for the scope of the issue", request.Action)
	case approval.RequestID != request.ID || approval.Nonce != request.Nonce:
		return refuse(GuardApproval, t, "the decision does not belong to the approval request")
	case approval.FactoryID != request.FactoryID || approval.IssueID != request.IssueID || approval.Action != request.Action:
		return refuse(GuardApproval, t, "the decision covers another factory, issue or action than the request")
	case approval.SubjectDigest != request.SubjectDigest || approval.PolicyRevision != request.PolicyRevision:
		return refuse(GuardApproval, t, "the decision covers another subject or policy than the request")
	case approval.ActorKind != core.ActorHuman:
		return refuse(GuardApproval, t, "an approval must be decided by a person, not by %s", string(approval.ActorKind))
	case approval.DecidedAt.After(now):
		return refuse(GuardApproval, t, "the approval is dated after the current instant")
	case approval.DecidedAt.Before(request.CreatedAt):
		return refuse(GuardApproval, t, "the approval is dated before its request was made")
	case request.Expired(approval.DecidedAt):
		return refuse(GuardApproval, t, "the approval was decided after its request had expired")
	}
	if request.Status != approvals.StatusApproved {
		message := "the approval request is %q, not approved"
		if request.Status == approvals.StatusConsumed {
			message = "the approval request is %q: the approval was already used"
		}
		refusal := refuse(GuardApproval, t, message, string(request.Status))
		refusal.Details["status"] = string(request.Status)
		return refusal
	}
	if err := approval.Authorizes(p.ScopeDigest, p.PolicyRevision, now); err != nil {
		refusal := refuse(GuardApproval, t, "the approval does not authorize the scope in force").WithCause(err)
		refusal.Details["cause"] = string(core.CodeOf(err))
		return refusal
	}
	return nil
}

package intake

import (
	"context"
	"fmt"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// draftOf turns a stored scope back into the draft it was written from.
func draftOf(sow SoW) Draft {
	return Draft{Kind: sow.Kind, Title: sow.Title, Scope: sow.Scope, DoD: sow.DoD, Features: sow.Features}
}

// planScope decides where a drafted scope goes: straight to Ready when it
// lies inside what agents may do on their own, otherwise into the approval
// inbox. The label that asked for intake plays no part in this.
func (s *Service) planScope(ctx context.Context, in input, snap snapshot) (*plan, error) {
	if snap.sow == nil {
		return nil, core.NewError(core.CodeInternal, "the issue has a drafted scope state but no stored scope")
	}
	sow := *snap.sow
	verdict := Classify(draftOf(sow), in.issue.AuthorAssociation, in.policy.Catalog)
	if !verdict.NeedsApproval {
		p, err := s.ready(snap, sow, core.EventScopePermitted, "the scope lies inside the delegated scope")
		if err != nil {
			return nil, err
		}
		p.delegation = delegated(in, sow)
		return p, nil
	}
	request, err := s.request(ctx, in, sow, verdict)
	if err != nil {
		return nil, err
	}
	doc := snap.doc
	doc.ApprovalID = request.ID
	p, err := s.step(snap, doc, core.EventApprovalRequired, riskSummary(verdict), sowRef(sow), "approval:"+request.ID)
	if err != nil {
		return nil, err
	}
	return p.with(waitingEffect(sow, request.ID, verdict))
}

// delegated is what the lifecycle machine is shown for a scope that needs
// nobody's approval: this scope, judged under the policy in force.
func delegated(in input, sow SoW) *statemachine.DelegationProof {
	return &statemachine.DelegationProof{IssueID: sow.Issue, ScopeDigest: sow.Digest, PolicyRevision: in.policy.Revision,
		Decision: core.AuthzDecision{Effect: core.EffectAllow, Reason: "the scope lies inside the delegated scope"}}
}

// waitingEffect queues the comment that says the scope waits for the Owner.
func waitingEffect(sow SoW, requestID string, verdict Classification) func() (state.OutboxMessage, error) {
	return func() (state.OutboxMessage, error) {
		return commentEffect(sow.Issue, "approval/"+requestID, approvalComment(sow, requestID, verdict))
	}
}

// ready moves the issue to Ready with exactly this scope version, and shows
// it on GitHub as approved.
func (s *Service) ready(snap snapshot, sow SoW, event core.Event, reason string, evidence ...string) (*plan, error) {
	doc := snap.doc
	doc.ApprovedVersion = sow.Version
	p, err := s.step(snap, doc, event, reason, append([]string{sowRef(sow)}, evidence...)...)
	if err != nil {
		return nil, err
	}
	return p.with(func() (state.OutboxMessage, error) {
		return labelEffect(sow.Issue, "ready/label", []string{github.LabelApproved}, nil)
	})
}

// blocked ends the wait for an approval that did not come, and records why,
// so that a later retry cannot stand in for the approval.
func (s *Service) blocked(in input, snap snapshot, sow SoW, request approvals.Request, reason DenialReason, why string) (*plan, error) {
	doc := snap.doc
	doc.Denial = &Denial{Reason: reason, RequestID: request.ID, ScopeDigest: sow.Digest, PolicyRevision: request.PolicyRevision, At: in.now}
	p, err := s.step(snap, doc, core.EventApprovalDenied, why, sowRef(sow), "approval:"+request.ID)
	if err != nil {
		return nil, err
	}
	issue := snap.doc.Issue
	return p.with(
		func() (state.OutboxMessage, error) {
			return labelEffect(issue, "blocked/label", []string{github.LabelBlocked}, nil)
		},
		func() (state.OutboxMessage, error) { return commentEffect(issue, "blocked", blockedComment(why)) },
	)
}

// planApproval decides what an issue waiting for the Owner does next.
//
// First the scope is compared with the issue as it reads now. An edit that
// changes the digest makes a new scope version with a new approval request;
// whatever was decided about the old version does not cover it. The same
// happens to the request when the policy in force was replaced meanwhile.
//
// Then the decision is read from the approval store, the only place one
// can come from. An approval becomes the step to Ready, which spends it on
// exactly this scope and policy as it is written. A rejection, a lapsed
// request and an approval that was used elsewhere block the issue. Anything
// else waits.
func (s *Service) planApproval(ctx context.Context, in input, snap snapshot) (*plan, error) {
	if snap.sow == nil || snap.doc.ApprovalID == "" {
		return nil, core.NewError(core.CodeInternal, "the issue waits for approval but has no stored scope or request")
	}
	sow := *snap.sow
	verdict := Classify(draftOf(sow), in.issue.AuthorAssociation, in.policy.Catalog)
	if draft := Analyze(in.issue, in.comments, in.labels); draft.Complete() {
		edited := Classify(draft, in.issue.AuthorAssociation, in.policy.Catalog)
		if ScopeDigest(sow.Issue, edited.Kind, draft) != sow.Digest {
			next := newSoW(sow.Issue, sow.Version+1, edited.Kind, draft, in.policy.Revision, in.now)
			return s.rerequest(ctx, in, snap, next, edited, true)
		}
	}
	request, err := s.cfg.Approvals.Get(ctx, snap.doc.ApprovalID)
	if err != nil {
		return nil, err
	}
	if request.PolicyRevision != in.policy.Revision && request.Status != approvals.StatusConsumed {
		return s.rerequest(ctx, in, snap, sow, verdict, false)
	}
	switch request.Status {
	case approvals.StatusExpired:
		return s.blocked(in, snap, sow, request, DenialExpired, "The approval request expired without a decision.")
	case approvals.StatusRejected:
		return s.blocked(in, snap, sow, request, DenialRejected, "The Owner rejected this scope.")
	case approvals.StatusConsumed:
		// Intake spends an approval in the step that makes the issue Ready,
		// so one that is spent while the issue still waits went elsewhere.
		return s.blocked(in, snap, sow, request, DenialUnusable, "The approval this issue waited on was used for something else.")
	case approvals.StatusChangesRequested:
		if snap.doc.NotedRequest == request.ID {
			return nil, nil
		}
		doc := snap.doc
		doc.NotedRequest = request.ID
		return (&plan{doc: doc}).with(func() (state.OutboxMessage, error) {
			return commentEffect(sow.Issue, "changes/"+request.ID, changesComment(sow))
		})
	case approvals.StatusApproved:
		return s.planReady(ctx, in, snap, sow, request)
	}
	return nil, nil
}

// planReady turns an approved request into the step to Ready. The approval
// is not spent here: the step names it, and writing the step spends it.
func (s *Service) planReady(ctx context.Context, in input, snap snapshot, sow SoW, request approvals.Request) (*plan, error) {
	decision, err := s.cfg.Approvals.Decision(ctx, request.ID)
	if err != nil {
		return nil, err
	}
	switch err := decision.Authorizes(sow.Digest, in.policy.Revision, in.now); {
	case core.HasCode(err, core.CodeExpired):
		return s.blocked(in, snap, sow, request, DenialExpired, "The approval expired before it was used.")
	case err != nil:
		return nil, err
	}
	p, err := s.ready(snap, sow, core.EventOwnerApproved, "the Owner approved this scope", "approval:"+request.ID)
	if err != nil {
		return nil, err
	}
	p.spend = spend(in, sow, request.ID)
	return p, nil
}

// spend names the approval a step uses up on one scope, next to the factory
// and the policy in force. Both are read from the policy, never from the
// request or the decision they are compared with.
func spend(in input, sow SoW, requestID string) *spending {
	return &spending{requestID: requestID, factory: in.policy.Manifest.FactoryID, scope: sow.Digest, revision: in.policy.Revision}
}

// rerequest points the issue at a new approval request: for a new scope
// version when the scope was edited, or for the same version under the
// policy now in force. The issue keeps waiting, and the request it waited on
// before is closed: a decision on it could never be used for this issue.
func (s *Service) rerequest(ctx context.Context, in input, snap snapshot, sow SoW, verdict Classification, edited bool) (*plan, error) {
	request, err := s.request(ctx, in, sow, verdict)
	if err != nil {
		return nil, err
	}
	doc := snap.doc
	doc.ApprovalID, doc.NotedRequest = request.ID, ""
	p := &plan{doc: doc, supersedes: snap.doc.ApprovalID}
	effects := []func() (state.OutboxMessage, error){waitingEffect(sow, request.ID, verdict)}
	if edited {
		doc.SoWVersion, doc.SoWDigest = sow.Version, sow.Digest
		p.doc, p.sow = doc, &sow
		effects = append([]func() (state.OutboxMessage, error){func() (state.OutboxMessage, error) {
			return commentEffect(sow.Issue, fmt.Sprintf("sow/%d", sow.Version), sowComment(sow))
		}}, effects...)
	}
	return p.with(effects...)
}

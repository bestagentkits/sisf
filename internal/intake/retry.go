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

// RetryRequest asks intake to send an issue back to Ready that it blocked
// because the scope did not get the approval it needed.
type RetryRequest struct {
	Issue core.IssueRef
	// Actor is the authenticated party asking, as the authentication layer
	// built it. It is never taken from the issue or from request content.
	Actor core.Actor
	// Decision is the answer of the authorization layer on that actor
	// retrying this issue. Only an outright allow counts.
	Decision core.AuthzDecision
}

// Retry sends an issue that intake blocked for want of an approval back to
// Ready, with the scope it was blocked with.
//
// Being allowed to retry does not undo the Owner's refusal. The scope needs
// the proof that would have made it Ready in the first place: an approval of
// exactly this scope, given in this factory under the policy in force and
// decided after the issue was blocked, or the policy's answer that the scope
// now lies inside what agents may do on their own. Intake reads both from
// the approval store and the policy; the request brings neither. The
// lifecycle machine judges them together with who asks: an agent cannot
// retry, and anything but an outright allow is refused. A refusal is
// GUARD_FAILED and changes nothing.
//
// An approval the retry uses is spent in the transaction that writes the
// step, so it lifts one block once. The step also removes the record of the
// denial: a later block of the issue has a cause of its own.
//
// An issue intake did not block for want of an approval is NOT_FOUND; its
// retry is judged by the rules behind RetryGuard.
func (s *Service) Retry(ctx context.Context, req RetryRequest) (Result, error) {
	result := Result{Issue: req.Issue}
	if err := req.Issue.Validate(); err != nil {
		return result, err
	}
	snap, err := s.load(ctx, req.Issue)
	if err != nil {
		return result, err
	}
	result.State = snap.record.State
	switch {
	case !snap.exists || snap.doc.Denial == nil:
		return result, core.NewError(core.CodeNotFound, "intake did not block this issue for want of an approval")
	case snap.sow == nil:
		return result, core.NewError(core.CodeInternal, "the issue was blocked for want of an approval but has no stored scope")
	}
	in, skipped, err := s.gather(ctx, req.Issue, true)
	if err != nil || skipped != "" {
		result.Skipped = skipped
		return result, err
	}
	p, err := s.planRetry(ctx, in, snap, req)
	if err != nil {
		return result, err
	}
	if err := s.apply(ctx, snap, p); err != nil {
		return result, err
	}
	result.State, result.Events = p.transition.To, []core.Event{p.transition.Event}
	return result, nil
}

// planRetry builds the step from Blocked back to Ready and gathers what the
// lifecycle machine is shown for it. Why and when the issue was blocked come
// from the record written with the block.
func (s *Service) planRetry(ctx context.Context, in input, snap snapshot, req RetryRequest) (*plan, error) {
	sow, denial := *snap.sow, *snap.doc.Denial
	doc := snap.doc
	doc.ApprovedVersion, doc.Denial = sow.Version, nil
	p, err := s.step(snap, doc, core.EventRetryAuthorized, "a retry was authorized and the scope has the approval it lacked", sowRef(sow))
	if err != nil {
		return nil, err
	}
	// An issue can be blocked and retried more than once, so the version it
	// is retried at tells the retries apart.
	p.transition.ActorID = req.Actor.ID
	p.transition.IdempotencyKey = fmt.Sprintf("intake/%s/%s/%d", sow.Issue, core.EventRetryAuthorized, snap.record.Version)
	p.retry = &statemachine.RetryProof{IssueID: sow.Issue, Cause: denial.Cause(), BlockedAt: denial.At,
		Actor: req.Actor, Decision: req.Decision}

	approval, found, err := s.approvalSince(ctx, in, sow)
	switch {
	case err != nil:
		return nil, err
	case found:
		p.doc.ApprovalID = approval.ID
		p.spend = spend(in, sow, approval.ID)
		p.transition.EvidenceRefs = append(p.transition.EvidenceRefs, "approval:"+approval.ID)
	case !Classify(draftOf(sow), in.issue.AuthorAssociation, in.policy.Catalog).NeedsApproval:
		p.delegation = delegated(in, sow)
	}
	return p.with(func() (state.OutboxMessage, error) {
		return labelEffect(sow.Issue, fmt.Sprintf("retry/%d/label", snap.record.Version),
			[]string{github.LabelApproved}, []string{github.LabelBlocked})
	})
}

// approvalSince looks in the approval store for an approval that could lift
// the block: an approved, unused request to approve exactly this scope of
// this issue, in this factory, under the policy in force. Of several it
// returns the one decided last, the only one that can have been decided
// after the block. Whether it was, and whether it holds, is for the
// lifecycle machine to judge.
func (s *Service) approvalSince(ctx context.Context, in input, sow SoW) (approvals.Request, bool, error) {
	approved, err := s.cfg.Approvals.List(ctx, approvals.Filter{IssueID: sow.Issue, Statuses: []approvals.Status{approvals.StatusApproved}})
	if err != nil {
		return approvals.Request{}, false, err
	}
	var (
		latest  approvals.Request
		decided approvals.Approval
		found   bool
	)
	for _, request := range approved {
		if request.Action != approvals.ActionApproveScope || request.FactoryID != in.policy.Manifest.FactoryID ||
			request.SubjectDigest != sow.Digest || request.PolicyRevision != in.policy.Revision {
			continue
		}
		decision, err := s.cfg.Approvals.Decision(ctx, request.ID)
		if err != nil {
			return approvals.Request{}, false, err
		}
		if !found || decision.DecidedAt.After(decided.DecidedAt) {
			latest, decided, found = request, decision, true
		}
	}
	return latest, found, nil
}

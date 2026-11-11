package intake

import (
	"context"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// RetryUnderPolicyInForce sends an issue back to Ready that was blocked
// because the policy its scope was written under was superseded while its
// run was under way. blockedAt is when it was blocked; the caller reads it
// from the record written with the block and vouches that this is why the
// issue is Blocked.
//
// A scope is permitted or approved under one policy, and a run is coded
// under the policy its scope names. So the retry does not carry the old
// permission over. The scope, unchanged, is judged under the policy in
// force as a scope is on its way to Ready: it needs that policy's answer
// that it lies inside what agents may do on their own, or an approval of
// exactly this scope under that policy which the Owner gave after the
// block. The lifecycle machine judges the proof with who asks; a refusal is
// GUARD_FAILED and changes nothing. RequestSupersededScopeApproval files
// the request for the approval when it is missing.
//
// The step stores the scope as a new version that names the policy in
// force, so the run that claims the issue next is coded under it, and
// spends the approval it used.
//
// An issue that is not Blocked with an approved scope and no denial is
// NOT_FOUND.
func (s *Service) RetryUnderPolicyInForce(ctx context.Context, req RetryRequest, blockedAt time.Time) (Result, error) {
	result := Result{Issue: req.Issue}
	if err := req.Issue.Validate(); err != nil {
		return result, err
	}
	snap, err := s.load(ctx, req.Issue)
	if err != nil {
		return result, err
	}
	result.State = snap.record.State
	if !supersededScope(snap) {
		return result, core.NewError(core.CodeNotFound, "the issue is not blocked with a scope that was approved under an earlier policy")
	}
	in, skipped, err := s.gather(ctx, req.Issue, true)
	if err != nil || skipped != "" {
		result.Skipped = skipped
		return result, err
	}
	if err := in.policy.Revision.Validate(); err != nil {
		return result, err
	}
	sow := *snap.sow
	doc := snap.doc
	repinned := sow.PolicyRevision != in.policy.Revision
	if repinned {
		sow = newSoW(sow.Issue, sow.Version+1, sow.Kind, draftOf(sow), in.policy.Revision, in.now)
		doc.SoWVersion, doc.SoWDigest = sow.Version, sow.Digest
	}
	doc.ApprovedVersion = sow.Version
	p, err := s.step(snap, doc, core.EventRetryAuthorized, "a retry was authorized and the scope is permitted under the policy in force", sowRef(sow))
	if err != nil {
		return result, err
	}
	p.transition.ActorID = req.Actor.ID
	p.transition.IdempotencyKey = fmt.Sprintf("intake/%s/%s/%d", sow.Issue, core.EventRetryAuthorized, snap.record.Version)
	p.retry = &statemachine.RetryProof{IssueID: sow.Issue, Cause: statemachine.BlockPolicySuperseded, BlockedAt: blockedAt,
		Actor: req.Actor, Decision: req.Decision}
	approval, found, err := s.approvalSince(ctx, in, sow)
	switch {
	case err != nil:
		return result, err
	case found:
		p.doc.ApprovalID = approval.ID
		p.spend = spend(in, sow, approval.ID)
		p.transition.EvidenceRefs = append(p.transition.EvidenceRefs, "approval:"+approval.ID)
	case !Classify(draftOf(sow), in.issue.AuthorAssociation, in.policy.Catalog).NeedsApproval:
		p.delegation = delegated(in, sow)
	}
	effects := []func() (state.OutboxMessage, error){func() (state.OutboxMessage, error) {
		return labelEffect(sow.Issue, fmt.Sprintf("retry/%d/label", snap.record.Version), []string{github.LabelApproved}, []string{github.LabelBlocked})
	}}
	if repinned {
		p.sow = &sow
		effects = append(effects, func() (state.OutboxMessage, error) {
			return commentEffect(sow.Issue, fmt.Sprintf("sow/%d", sow.Version), sowComment(sow))
		})
	}
	if p, err = p.with(effects...); err != nil {
		return result, err
	}
	if err := s.apply(ctx, snap, p); err != nil {
		return result, err
	}
	result.State, result.Events = p.transition.To, []core.Event{p.transition.Event}
	return result, nil
}

// supersededScope reports whether snap is an issue a run was stopped on:
// Blocked, with the scope it became Ready with and no approval it lacked.
func supersededScope(snap snapshot) bool {
	return snap.exists && snap.record.State == core.StateBlocked && snap.doc.Denial == nil &&
		snap.sow != nil && snap.doc.ApprovedVersion != 0 && snap.doc.ApprovedVersion == snap.sow.Version
}

package intake

import (
	"context"
	"fmt"
	"strings"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// request files the approval request for one scope version under the policy
// in force, or returns the one already filed. It asks for the one action
// that lets a scope go ahead, approvals.ActionApproveScope. The ID is derived
// from the issue, the scope digest and the policy revision, so filing is safe
// to repeat and a request can never be mistaken for one about other content.
func (s *Service) request(ctx context.Context, in input, sow SoW, verdict Classification) (approvals.Request, error) {
	revision := in.policy.Revision
	if err := revision.Validate(); err != nil {
		return approvals.Request{}, err
	}
	id := fmt.Sprintf("intake:%s:%s:%s", sow.Issue, strings.TrimPrefix(string(sow.Digest), "sha256:")[:16], string(revision)[:12])
	existing, err := s.cfg.Approvals.Get(ctx, id)
	switch {
	case err == nil && (existing.SubjectDigest != sow.Digest || existing.PolicyRevision != revision):
		return approvals.Request{}, core.NewError(core.CodeConflict, "another approval request already uses this id")
	case err == nil:
		return existing, nil
	case !core.HasCode(err, core.CodeNotFound):
		return approvals.Request{}, err
	}
	nonce, err := approvals.NewNonce()
	if err != nil {
		return approvals.Request{}, err
	}
	created, err := s.cfg.Approvals.Create(ctx, approvals.Request{
		ID: id, FactoryID: in.policy.Manifest.FactoryID, IssueID: sow.Issue, Action: approvals.ActionApproveScope,
		SubjectDigest: sow.Digest, PolicyRevision: revision, Nonce: nonce, ExpiresAt: in.now.Add(s.cfg.ApprovalTTL),
		RequestedBy: serviceActor, RiskSummary: riskSummary(verdict), Diff: sow.Text(), AffectedFeatures: sow.Features,
		EvidenceRefs: []string{"issue:" + sow.Issue.String(), sowRef(sow)},
		// Letting work start that nobody was authorized to ask for is a
		// decision for a person who has just confirmed who they are.
		StepUp: true, Status: approvals.StatusPending, CreatedAt: in.now,
	})
	if core.HasCode(err, core.CodeAlreadyExists) {
		return s.cfg.Approvals.Get(ctx, id)
	}
	return created, err
}

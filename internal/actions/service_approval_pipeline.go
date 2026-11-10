package actions

import (
	"context"
	"fmt"
	"strings"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// maxPlanAttempts bounds how many approval requests one exact change may
// file in its life: each used, rejected or lapsed request moves to the next
// slot.
const maxPlanAttempts = 20

// plan files the approval request a sensitive call needs and answers
// APPROVAL_REQUIRED naming it. The request is bound to the action, the exact
// input and the policy in force; the risk summary and the diff come from the
// handler's own dry run. Planning the same change again while its request is
// pending or approved returns that request instead of filing another.
func (s *Service) plan(ctx context.Context, call invocation) error {
	request, err := s.fileRequest(ctx, call)
	if err != nil {
		s.audit(ctx, call, core.OutcomeFailed, "plan", binding{}, err)
		return err
	}
	call.req.ApprovalID = request.ID
	s.audit(ctx, call, core.OutcomeAllowed, "plan", binding{digest: request.SubjectDigest, policy: request.PolicyRevision}, nil)
	e := core.NewError(core.CodeApprovalRequired, call.spec.Name+" waits for an Owner approval",
		core.NextAction{Action: "approvals.get", Description: "read the filed request", Params: map[string]string{"id": request.ID}},
		core.NextAction{Action: ApprovalDecisionAction, Description: "the Owner approves or rejects it", Params: map[string]string{"request_id": request.ID}},
		core.NextAction{Action: call.spec.Name, Description: "once approved, call again with the same input and this approval_id",
			Params: map[string]string{"approval_id": request.ID}})
	e.Details = map[string]string{"approval_id": request.ID, "subject_digest": string(request.SubjectDigest),
		"policy_revision": string(request.PolicyRevision), "expires_at": request.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")}
	return e
}

func (s *Service) fileRequest(ctx context.Context, call invocation) (approvals.Request, error) {
	policy, err := s.policyRevision(ctx)
	if err != nil {
		return approvals.Request{}, err
	}
	digest, err := subjectDigest(s.cfg.FactoryID, call.spec.Name, call.req.Input)
	if err != nil {
		return approvals.Request{}, err
	}
	preview := call
	preview.req.DryRun = true
	planned, err := s.dryRun(ctx, preview, true)
	if err != nil {
		return approvals.Request{}, err
	}
	for n := 1; n <= maxPlanAttempts; n++ {
		id := requestID(digest, n)
		existing, err := s.cfg.Approvals.Get(ctx, id)
		switch {
		case err == nil && (existing.Action != call.spec.Name || existing.SubjectDigest != digest):
			return approvals.Request{}, core.NewError(core.CodeConflict, "another approval request already uses this id")
		case err == nil && existing.PolicyRevision == policy &&
			(existing.Status == approvals.StatusPending || existing.Status == approvals.StatusApproved):
			return existing, nil
		case err == nil:
			continue
		case !core.HasCode(err, core.CodeNotFound):
			return approvals.Request{}, err
		}
		nonce, err := approvals.NewNonce()
		if err != nil {
			return approvals.Request{}, err
		}
		now := s.cfg.Clock.Now()
		created, err := s.cfg.Approvals.Create(ctx, approvals.Request{
			ID: id, FactoryID: s.cfg.FactoryID, Action: call.spec.Name, SubjectDigest: digest, PolicyRevision: policy,
			Nonce: nonce, ExpiresAt: now.Add(s.cfg.RequestTTL), RequestedBy: call.req.Actor.ID,
			RiskSummary: riskSummary(call.spec, planned.Plan), Diff: renderDiff(planned.Plan),
			EvidenceRefs: []string{"action:" + call.spec.Name, "policy:" + string(policy)},
			StepUp:       call.spec.StepUp, Status: approvals.StatusPending, CreatedAt: now,
		})
		if core.HasCode(err, core.CodeAlreadyExists) {
			// Another caller filed the same change at the same moment.
			return s.cfg.Approvals.Get(ctx, id)
		}
		return created, err
	}
	return approvals.Request{}, core.NewError(core.CodeLimitExceeded, "the Owner was asked about this exact change too many times")
}

// executeApproved spends the approval on the exact input and policy, then
// runs the handler. The use is announced in the audit log first and nothing
// runs when that fails. A refused approval is left unspent. Once spent, the
// approval is gone even if the handler then fails: the action is never run
// twice on one approval.
func (s *Service) executeApproved(ctx context.Context, call invocation) (core.ActionResult, error) {
	policy, err := s.policyRevision(ctx)
	if err != nil {
		return core.ActionResult{}, err
	}
	digest, err := subjectDigest(s.cfg.FactoryID, call.spec.Name, call.req.Input)
	if err != nil {
		return core.ActionResult{}, err
	}
	bound := binding{digest: digest, policy: policy}
	if _, err := s.appendBound(ctx, call, core.OutcomeAllowed, "execute", bound, nil); err != nil {
		return core.ActionResult{}, core.NewError(core.CodeUnavailable, "the execution could not be audited, so nothing ran").WithCause(err)
	}
	if _, err := s.cfg.Approvals.Consume(ctx, call.req.ApprovalID, digest, policy); err != nil {
		s.audit(ctx, call, core.OutcomeDenied, "execute", bound, err)
		return core.ActionResult{}, approvalRefusal(call, err)
	}
	return s.execute(ctx, call, bound)
}

// binding is what an executed sensitive call was approved for; it is zero
// for a call that needed no approval.
type binding struct {
	digest core.Digest
	policy core.CommitSHA
}

// execute runs a write and records its outcome. The audit id of the record
// is returned with the result; when the record cannot be written the result
// stands but carries no audit id.
func (s *Service) execute(ctx context.Context, call invocation, bound binding) (core.ActionResult, error) {
	call.req.DryRun = false
	res, err := call.handle(ctx, call.req)
	outcome := core.OutcomeExecuted
	if err != nil {
		outcome = core.OutcomeFailed
	}
	// The outcome is decided; a record that cannot be written is caught by
	// the log's own verification, and the result then carries no audit id.
	record, _ := s.appendBound(ctx, call, outcome, "execute", bound, err)
	if err != nil {
		return core.ActionResult{}, err
	}
	res.Plan = nil
	if record.Seq > 0 {
		res.AuditID = fmt.Sprintf("audit-%d", record.Seq)
	}
	return res, nil
}

// approvalRefusal explains a refused approval with the step that is open.
func approvalRefusal(call invocation, err error) error {
	switch core.CodeOf(err) {
	case core.CodeApprovalRequired, core.CodeReplayed, core.CodeExpired:
		return core.NewError(core.CodeOf(err), messageOf(err),
			core.NextAction{Action: call.spec.Name, Description: "call again without approval_id to plan this exact input"}).WithCause(err)
	case core.CodeNotFound:
		return core.NewError(core.CodeNotFound, "no approval request has this id",
			core.NextAction{Action: "approvals.list", Description: "find the approval request filed for this change"},
			core.NextAction{Action: call.spec.Name, Description: "call again without approval_id to plan it"}).WithCause(err)
	}
	return err
}

func (s *Service) policyRevision(ctx context.Context) (core.CommitSHA, error) {
	policy, err := s.cfg.Policy.PolicyRevision(ctx)
	if err != nil {
		return "", core.NewError(core.CodePolicyHold, "the policy in force could not be read, so nothing sensitive can run").WithCause(err)
	}
	if err := policy.Validate(); err != nil {
		return "", core.NewError(core.CodePolicyHold, "the policy in force has no valid revision").WithCause(err)
	}
	return policy, nil
}

// riskSummary is the sentence the approver reads first: the handler's first
// warning, else what the action does.
func riskSummary(spec core.ActionSpec, plan *core.DryRun) string {
	if plan != nil && len(plan.Warnings) > 0 && strings.TrimSpace(plan.Warnings[0]) != "" {
		return plan.Warnings[0]
	}
	return "Runs " + spec.Name + ": " + spec.Summary
}

// renderDiff lists each planned change on one line.
func renderDiff(plan *core.DryRun) string {
	if plan == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range plan.Changes {
		fmt.Fprintf(&b, "%s %s", c.Op, c.Resource)
		if len(c.Before) > 0 {
			fmt.Fprintf(&b, " before=%s", c.Before)
		}
		if len(c.After) > 0 {
			fmt.Fprintf(&b, " after=%s", c.After)
		}
		b.WriteString("\n")
	}
	return b.String()
}

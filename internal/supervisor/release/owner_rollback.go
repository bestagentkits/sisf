package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// RollbackAction is the approval action an Owner approves to have a unit
// restored to its last known good release.
const RollbackAction = "deployments.rollback"

// Domain and version of the payload a rollback approval's subject digests.
const (
	rollbackSubjectDomain  = "sisf.rollback_subject"
	rollbackSubjectVersion = 1
)

// RollbackSubject is the subject digest of an approval to restore unit of
// factory to the release to. An approval for one target never covers
// another: once the LKG moves, an older approval no longer applies.
func RollbackSubject(factory core.FactoryID, unit string, to core.ReleaseDigest) core.Digest {
	payload := core.NewPayloadBuilder(rollbackSubjectDomain, rollbackSubjectVersion).
		Text("factory_id", string(factory)).Text("unit", unit).Text("to", string(to)).Bytes()
	sum := sha256.Sum256(payload)
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// ApprovalSpender spends a signed, single-use Owner approval. The approval
// store of package approvals implements it: it verifies the decision's
// signature, that the Owner decided it, its binding to subject and policy
// and its expiry, and marks it used in the same transaction.
type ApprovalSpender interface {
	Consume(ctx context.Context, requestID string, subject core.Digest, policy core.CommitSHA) (approvals.Approval, error)
}

// PolicyInForce reads the policy commit in force now.
type PolicyInForce func(ctx context.Context) (core.CommitSHA, error)

// OwnerRollback restores a unit to its LKG when the Owner approved exactly
// that. The approval must be for this factory, the rollback action and the
// current LKG of the unit under the policy in force, unexpired and unused;
// it is spent before anything changes, so it cannot be replayed. The LKG is
// then observed running healthy, read from the running system, or the
// rollback is reported as not done. Data volumes are never touched. A
// rollback is the safe direction, so an emergency stop does not prevent it,
// and neither does the budget: its spend is recorded like the automatic
// restore's, and an exhausted or unavailable budget is audited, not obeyed.
func (p *Pipeline) OwnerRollback(ctx context.Context, req supervisor.RollbackRequest) (supervisor.RollbackReport, error) {
	if err := req.Validate(); err != nil {
		return supervisor.RollbackReport{}, err
	}
	if p.cfg.Approvals == nil || p.cfg.Policy == nil {
		return supervisor.RollbackReport{}, core.NewError(core.CodeNotSupported, "this supervisor cannot verify Owner approvals")
	}
	if req.FactoryID != p.cfg.FactoryID {
		return supervisor.RollbackReport{}, core.NewError(core.CodePermissionDenied, "the rollback is asked for another factory")
	}
	lkg, found, err := p.cfg.LKG.Get(ctx, req.Unit)
	switch {
	case err != nil:
		return supervisor.RollbackReport{}, holdf("the last known good release of %s cannot be read", req.Unit).WithCause(err)
	case !found:
		return supervisor.RollbackReport{}, holdf("unit %s has no last known good release to roll back to", req.Unit)
	}
	policy, err := p.cfg.Policy(ctx)
	if err != nil {
		return supervisor.RollbackReport{}, core.NewError(core.CodePolicyHold, "the policy in force cannot be read").WithCause(err)
	}
	approval, err := p.cfg.Approvals.Consume(ctx, req.ApprovalRef, RollbackSubject(p.cfg.FactoryID, req.Unit, lkg.Digest), policy)
	if err == nil && (approval.Action != RollbackAction || approval.FactoryID != p.cfg.FactoryID) {
		err = core.NewError(core.CodeApprovalRequired, "the approval was given for another action")
	}
	if err != nil {
		p.auditRollback(ctx, req, core.OutcomeDenied, string(core.CodeOf(err)))
		return supervisor.RollbackReport{}, err
	}
	report := supervisor.RollbackReport{Unit: req.Unit, RestoredTo: lkg.Digest}
	if before, err := p.cfg.Backend.ObservedRevision(ctx, req.Unit); err == nil {
		report.From = before.Digest
	}
	target := deploy.Revision{Unit: req.Unit, Digest: lkg.Digest}
	charge := rollbackCharge{issue: approval.IssueID, audit: func(ctx context.Context, outcome core.AuditOutcome, detail string) {
		p.auditRollback(ctx, req, outcome, detail)
	}}
	spend := p.reserveRollback(ctx, charge)
	err = p.cfg.Backend.Rollback(ctx, target)
	p.recordRollbackSpend(ctx, charge, spend)
	if err != nil {
		p.auditRollback(ctx, req, core.OutcomeFailed, "rollback")
		return report, holdf("the last known good release could not be restored").WithCause(err)
	}
	observed, err := p.cfg.Backend.ObservedRevision(ctx, req.Unit)
	if err != nil || !observed.Matches(target) {
		p.auditRollback(ctx, req, core.OutcomeFailed, "observe")
		return report, holdf("the last known good release is not observed running and healthy after the restore")
	}
	report.Observed, report.Healthy, report.At = observed.Digest, observed.Healthy, observed.ObservedAt
	p.auditRollback(ctx, req, core.OutcomeExecuted, "")
	return report, nil
}

// auditRollback records an Owner rollback request and how it ended. The
// outcome does not depend on the audit log.
func (p *Pipeline) auditRollback(ctx context.Context, req supervisor.RollbackRequest, outcome core.AuditOutcome, detail string) {
	if p.cfg.Audit == nil {
		return
	}
	details := map[string]string{"unit": req.Unit, "approval": req.ApprovalRef}
	if detail != "" {
		details["detail"] = detail
	}
	_, _ = p.cfg.Audit.Append(ctx, core.AuditEvent{
		FactoryID: p.cfg.FactoryID, ActorID: actorID, ActorKind: core.ActorService, Action: RollbackAction,
		Resource: "deployments", Outcome: outcome, Details: details,
	})
}

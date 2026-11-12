package egress

import (
	"context"
	"strings"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Audited actions.
const (
	actionPlan    = "egress.policy.plan"
	actionExecute = "egress.policy.execute"
)

// Plan files the approval request a change to the egress policy needs and
// returns it with the change and, for a change that widens the policy or
// turns blocking off, the risk warning the Owner reads first.
//
// An agent may only propose to narrow the policy (PERMISSION_DENIED
// otherwise); anyone else must be an Owner or a Maintainer. A proposal that
// changes nothing is CONFLICT. Proposing the same change again while its
// request is open or approved returns that request.
func (s *Service) Plan(ctx context.Context, actor core.Actor, proposed core.EgressPolicy) (Plan, error) {
	plan, err := s.plan(ctx, actor, proposed)
	s.audit(ctx, actor, actionPlan, plan.Request.ID, plan.Request.SubjectDigest, plan.Request.PolicyRevision, plan.Change, err)
	return plan, err
}

func (s *Service) plan(ctx context.Context, actor core.Actor, proposed core.EgressPolicy) (Plan, error) {
	proposed = Normalize(proposed)
	if err := proposed.Validate(); err != nil {
		return Plan{}, err
	}
	current, err := s.Current(ctx)
	if err != nil {
		return Plan{}, err
	}
	change := Compare(current.Policy, proposed)
	out := Plan{Current: current.Policy, Proposed: proposed, Change: change, RiskWarning: RiskWarning(change)}
	if err := mayChange(actor, change); err != nil {
		return out, err
	}
	if change.Empty() {
		return out, core.NewError(core.CodeConflict, "the proposed egress policy is the one in force")
	}
	digest, err := subjectDigest(current.Revision, current.version, proposed)
	if err != nil {
		return out, err
	}
	out.Request.SubjectDigest, out.Request.PolicyRevision = digest, current.Revision
	doc := storedPlan{Proposed: proposed, Change: change, BaseRevision: current.Revision, BaseVersion: current.version,
		Digest: digest, RequestedBy: actor.ID}
	for n := 1; n <= maxPlanRetries; n++ {
		request, err := s.file(ctx, actor, planID(digest, n), doc, out.RiskWarning)
		if err != nil {
			return out, err
		}
		if request.Status == approvals.StatusPending || request.Status == approvals.StatusApproved {
			out.Request = request
			return out, nil
		}
	}
	return out, core.NewError(core.CodeLimitExceeded, "the Owner was asked about this egress change too many times")
}

// file stores the plan and its approval request under id, or returns the
// request already filed there.
func (s *Service) file(ctx context.Context, actor core.Actor, id string, doc storedPlan, warning string) (approvals.Request, error) {
	existing, err := s.cfg.Approvals.Get(ctx, id)
	switch {
	case err == nil && (existing.Action != ActionChangePolicy || existing.SubjectDigest != doc.Digest):
		return approvals.Request{}, core.NewError(core.CodeConflict, "another approval request already uses this id")
	case err == nil:
		return existing, nil
	case !core.HasCode(err, core.CodeNotFound):
		return approvals.Request{}, err
	}
	err = s.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		var old storedPlan
		version, err := loadDoc(ctx, tx, planPrefix+id, &old)
		if err != nil {
			return err
		}
		return putDoc(ctx, tx, planPrefix+id, version, doc)
	})
	if err != nil {
		return approvals.Request{}, err
	}
	nonce, err := approvals.NewNonce()
	if err != nil {
		return approvals.Request{}, err
	}
	summary := warning
	if summary == "" {
		summary = "Narrows the egress policy of agent sandboxes; destinations removed from it will be blocked."
	}
	now := s.cfg.Clock.Now()
	created, err := s.cfg.Approvals.Create(ctx, approvals.Request{
		ID: id, FactoryID: s.cfg.FactoryID, Action: ActionChangePolicy, SubjectDigest: doc.Digest,
		PolicyRevision: doc.BaseRevision, Nonce: nonce, ExpiresAt: now.Add(s.cfg.RequestTTL), RequestedBy: actor.ID,
		RiskSummary: summary, Diff: doc.Change.String(), EvidenceRefs: []string{"policy:" + string(doc.BaseRevision)},
		StepUp: true, Status: approvals.StatusPending, CreatedAt: now,
	})
	if core.HasCode(err, core.CodeAlreadyExists) {
		return s.cfg.Approvals.Get(ctx, id)
	}
	return created, err
}

// Execute puts an approved change in force and spends its approval, in one
// transaction. The change must still apply to the policy it was planned
// against: after another change or a new policy revision it is CONFLICT
// and must be planned again. An agent cannot execute a change that widens
// the policy, approved or not. Every attempt is audited.
func (s *Service) Execute(ctx context.Context, actor core.Actor, requestID string) (Effective, error) {
	var doc storedPlan
	err := s.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		version, err := loadDoc(ctx, tx, planPrefix+requestID, &doc)
		if err == nil && version == 0 {
			err = core.NewError(core.CodeNotFound, "no egress change was planned under this request")
		}
		return err
	})
	if err == nil {
		err = mayChange(actor, doc.Change)
	}
	if err == nil {
		err = s.execute(ctx, requestID, doc)
	}
	s.audit(ctx, actor, actionExecute, requestID, doc.Digest, doc.BaseRevision, doc.Change, err)
	if err != nil {
		return Effective{}, err
	}
	return s.Current(ctx)
}

func (s *Service) execute(ctx context.Context, requestID string, doc storedPlan) error {
	current, err := s.Current(ctx)
	if err != nil {
		return err
	}
	if current.Revision != doc.BaseRevision {
		return core.NewError(core.CodeConflict, "a new policy revision was approved since this change was planned; plan it again")
	}
	_, err = s.cfg.Approvals.ConsumeWith(ctx, requestID, doc.Digest, doc.BaseRevision,
		func(ctx context.Context, tx state.Tx, _ approvals.Request, a approvals.Approval) error {
			var stored storedPolicy
			version, err := loadDoc(ctx, tx, policyKey, &stored)
			if err != nil {
				return err
			}
			if version != doc.BaseVersion {
				return core.NewError(core.CodeConflict, "the egress policy changed since this change was planned; plan it again")
			}
			return putDoc(ctx, tx, policyKey, version, storedPolicy{Policy: doc.Proposed, BaseRevision: doc.BaseRevision,
				RequestID: requestID, ApprovedBy: a.ActorID, AppliedAt: s.cfg.Clock.Now()})
		})
	return err
}

// audit records a plan or an execution. The record names hosts, never
// anything a runner sent.
func (s *Service) audit(ctx context.Context, actor core.Actor, action, requestID string, digest core.Digest,
	revision core.CommitSHA, change Change, failure error) {
	outcome := core.OutcomeExecuted
	details := map[string]string{"widens": boolText(change.Widens())}
	if requestID != "" {
		details["request_id"] = requestID
	}
	if len(change.Added) > 0 {
		details["added"] = strings.Join(change.Added, ",")
	}
	if change.TurnsOffBlocking() {
		details["turns_off_blocking"] = "true"
	}
	if failure != nil {
		outcome = core.OutcomeDenied
		if code := core.CodeOf(failure); code != core.CodePermissionDenied && code != core.CodeUnauthenticated {
			outcome = core.OutcomeFailed
		}
		details["code"] = string(core.CodeOf(failure))
	}
	kind := actor.Kind
	if !kind.Valid() {
		kind = core.ActorService
	}
	actorID := actor.ID
	if actorID == "" {
		actorID = "unidentified"
	}
	event := core.AuditEvent{FactoryID: s.cfg.FactoryID, ActorID: actorID, ActorKind: kind, Action: action,
		Resource: "egress.policy", Outcome: outcome, Details: details}
	if actor.Surface.Valid() {
		event.Surface = actor.Surface
	}
	if digest.Validate() == nil {
		event.SubjectDigest = digest
	}
	if revision.Validate() == nil {
		event.PolicySHA = revision
	}
	// The outcome is already decided; a failed audit append is reported by
	// the log's own verification, not by undoing the decision.
	_, _ = s.cfg.Audit.Append(ctx, event)
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

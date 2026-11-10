package actions

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
)

// audit records one step of a call. Failures to write are not returned: the
// outcome is already decided, and a gap shows up in the log's verification.
func (s *Service) audit(ctx context.Context, call invocation, outcome core.AuditOutcome, phase string, bound binding, cause error) {
	_, _ = s.appendBound(ctx, call, outcome, phase, bound, cause)
}

// appendBound writes one audit record about a call. phase names the step of
// the pipeline: authorize, plan or execute. The record names the actor and
// the approval, never the input, which may carry data the log must not keep.
func (s *Service) appendBound(ctx context.Context, call invocation, outcome core.AuditOutcome, phase string,
	bound binding, cause error,
) (core.AuditEvent, error) {
	actor := call.req.Actor
	event := core.AuditEvent{
		At: s.cfg.Clock.Now(), FactoryID: s.cfg.FactoryID, ActorID: actor.ID, ActorKind: actor.Kind,
		Action: call.spec.Name, Resource: "action:" + call.spec.Name, Outcome: outcome,
		Details: map[string]string{"phase": phase, "sensitivity": string(call.spec.Sensitivity)},
	}
	if event.ActorID == "" {
		event.ActorID = "unidentified"
	}
	if !event.ActorKind.Valid() {
		event.ActorKind = core.ActorService
	}
	if actor.Surface.Valid() {
		event.Surface = actor.Surface
	}
	if actor.SessionID != "" {
		event.Details["session_id"] = actor.SessionID
	}
	if call.req.ApprovalID != "" {
		event.Details["approval_id"] = call.req.ApprovalID
	}
	if call.req.IdempotencyKey != "" {
		event.Details["idempotency_key"] = call.req.IdempotencyKey
	}
	if bound.digest.Validate() == nil {
		event.SubjectDigest = bound.digest
	}
	if bound.policy.Validate() == nil {
		event.PolicySHA = bound.policy
	}
	if cause != nil {
		event.Details["code"] = string(core.CodeOf(cause))
	}
	return s.cfg.Audit.Append(ctx, event)
}

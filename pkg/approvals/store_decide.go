package approvals

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// approverKey is the context key of the authenticated approver.
type approverKey struct{}

// WithApprover returns a context that carries the actor a decision is
// recorded for. The actor comes from a verified session or token, never from
// request content. Decide refuses a call whose context carries none, so a
// decision cannot be recorded by naming someone in its fields.
func WithApprover(ctx context.Context, actor core.Actor) context.Context {
	return context.WithValue(ctx, approverKey{}, actor)
}

// mayDecide reports whether the authenticated approver of ctx may record a.
// Without an identified approver the call is UNAUTHENTICATED. A decision that
// names someone else, or an approver who is not an Owner, is
// PERMISSION_DENIED.
func mayDecide(ctx context.Context, a Approval) error {
	actor, ok := ctx.Value(approverKey{}).(core.Actor)
	switch {
	case !ok || actor.ID == "" || !actor.Kind.Valid() || !actor.Surface.Valid():
		return core.NewError(core.CodeUnauthenticated, "the approver could not be identified")
	case actor.ID != a.ActorID || actor.Kind != a.ActorKind || actor.Surface != a.Surface || !actor.StepUpAt.Equal(a.StepUpAt):
		return core.NewError(core.CodePermissionDenied, "the decision names someone other than the authenticated approver")
	case !actor.HasRole(core.RoleOwner):
		return core.NewError(core.CodePermissionDenied, "only the Owner may decide an approval request")
	}
	return nil
}

// Decide records a signed decision on a pending request.
//
// The decision is recorded for the approver the context carries, see
// WithApprover and Submit. Only an Owner may decide: a call without an
// identified approver is UNAUTHENTICATED, and a decision that names someone
// else or comes from another role is PERMISSION_DENIED. Every decision in the
// store was therefore made by an actor who was authorized to make it.
//
// The remaining checks run in a fixed order and the first failure wins: a
// request that was already decided or used, or a nonce that is not the
// request's, is REPLAYED; a lapsed request is EXPIRED; a request of another
// factory than this store serves is PERMISSION_DENIED; a factory, issue,
// action, subject or policy revision other than the request's is
// APPROVAL_REQUIRED, because the approver looked at something else; a
// malformed decision, or one made before its request, is INVALID_ARGUMENT;
// and a request marked StepUp accepts only a person who confirmed their
// identity within the step-up age, otherwise STEP_UP_REQUIRED.
//
// The store stamps the decision time, caps the expiry at the request's and
// signs Approval.SigningPayload itself. A time or a signature supplied by the
// caller is discarded, so a decision can be neither backdated nor forged, and
// every recorded decision was made while its request was open.
func (s *KVStore) Decide(ctx context.Context, a Approval) (Request, error) {
	return s.decide(ctx, a, false)
}

// decide is Decide. With answering set, the decision takes the factory, the
// issue and the action from the request it answers instead of stating them.
func (s *KVStore) decide(ctx context.Context, a Approval, answering bool) (Request, error) {
	actor := auditActor{id: a.ActorID, kind: a.ActorKind, surface: a.Surface,
		details: map[string]string{"decision": string(a.Decision)}}
	if denied := mayDecide(ctx, a); denied != nil {
		if core.HasCode(denied, core.CodeUnauthenticated) {
			return Request{}, denied
		}
		request, err := s.Get(ctx, a.RequestID)
		if err != nil {
			return Request{}, err
		}
		return Request{}, s.record(ctx, request, actionDecide, actor, denied)
	}
	var request, decided Request
	err := s.update(ctx, func(ctx context.Context, tx state.Tx) error {
		r, version, err := loadRequest(ctx, tx, a.RequestID)
		if err != nil {
			return err
		}
		request = r
		answer := a
		if answering {
			answer.FactoryID, answer.IssueID, answer.Action = r.FactoryID, r.IssueID, r.Action
		}
		signed, err := s.judge(ctx, s.current(r), answer)
		if err != nil {
			return err
		}
		r.Status = statusOf(signed.Decision)
		if err := put(ctx, tx, decisionKey(r.ID), 0, signed); err != nil {
			return err
		}
		if err := put(ctx, tx, requestKey(r.ID), version, r); err != nil {
			return err
		}
		decided = r
		return nil
	})
	if err := s.record(ctx, request, actionDecide, actor, err); err != nil {
		return Request{}, err
	}
	return decided, nil
}

// judge decides whether a may be recorded on r and returns it stamped and
// signed. It writes nothing.
func (s *KVStore) judge(ctx context.Context, r Request, a Approval) (Approval, error) {
	now := s.clock.Now()
	switch {
	case r.Status != StatusPending && r.Status != StatusExpired:
		return Approval{}, core.NewError(core.CodeReplayed, "the approval request was already decided")
	case a.Nonce != r.Nonce:
		return Approval{}, core.NewError(core.CodeReplayed, "the nonce does not belong to this approval request")
	case r.Status == StatusExpired:
		return Approval{}, core.NewError(core.CodeExpired, "the approval request has lapsed")
	case r.FactoryID != s.factory:
		return Approval{}, core.NewError(core.CodePermissionDenied,
			"the approval request belongs to another factory than this store serves")
	case a.FactoryID != r.FactoryID || a.IssueID != r.IssueID || a.Action != r.Action:
		return Approval{}, core.NewError(core.CodeApprovalRequired,
			"the decision names a different factory, issue or action than the request")
	case a.SubjectDigest != r.SubjectDigest || a.PolicyRevision != r.PolicyRevision:
		return Approval{}, core.NewError(core.CodeApprovalRequired,
			"the decision names a different subject or policy revision than the request")
	case now.Before(r.CreatedAt):
		// A request dated ahead of this clock is not open yet. A decision
		// stamped now would predate it and could never be used.
		return Approval{}, invalid("the approval request is dated after the current instant and cannot be decided yet")
	}
	a.DecidedAt, a.Signature = now, core.Signature{}
	if a.ExpiresAt.IsZero() || a.ExpiresAt.After(r.ExpiresAt) {
		a.ExpiresAt = r.ExpiresAt
	}
	if err := a.Validate(); err != nil {
		return Approval{}, err
	}
	if r.StepUp && !s.freshStepUp(a, now) {
		return Approval{}, core.NewError(core.CodeStepUpRequired,
			"this approval needs a person who has just confirmed their identity")
	}
	signature, err := s.signer.Sign(ctx, a.SigningPayload())
	if err != nil {
		return Approval{}, err
	}
	if err := signature.Validate(); err != nil {
		return Approval{}, core.NewError(core.CodeSignatureInvalid, "the decision could not be signed").WithCause(err)
	}
	a.Signature = signature
	return a, nil
}

// freshStepUp reports whether a person confirmed their identity recently
// enough. An agent or a service never satisfies it, whatever session it uses.
func (s *KVStore) freshStepUp(a Approval, now time.Time) bool {
	return a.ActorKind == core.ActorHuman && !a.StepUpAt.IsZero() &&
		!a.StepUpAt.After(now) && now.Sub(a.StepUpAt) <= s.stepUp
}

// statusOf maps a valid decision to the status the request takes.
func statusOf(d Decision) Status {
	switch d {
	case DecisionApprove:
		return StatusApproved
	case DecisionReject:
		return StatusRejected
	}
	return StatusChangesRequested
}

// Submission is what an approver sends back after reading a request. Nonce,
// SubjectDigest and PolicyRevision echo what the approver was shown, which is
// how a decision made on an outdated view is caught.
type Submission struct {
	RequestID      string
	Nonce          string
	SubjectDigest  core.Digest
	PolicyRevision core.CommitSHA
	Decision       Decision
	Reason         string
	Conditions     Conditions
}

// Submit records the decision of an authenticated actor. It is the entry
// point for every surface: the actor comes from a verified session or token,
// never from request content.
//
// Who decided, as what kind of actor and through which surface is taken from
// the actor, so a caller cannot claim to be someone else. The factory, the
// issue and the action are those of the request the submission answers: they
// are fixed when a request is filed, so there is no other view of them an
// approver could have decided on. Decide then applies its rules: only an
// identified Owner gets as far as the request.
func (s *KVStore) Submit(ctx context.Context, actor core.Actor, sub Submission) (Request, error) {
	if sub.RequestID == "" {
		return Request{}, invalid("a decision needs the id of the request it answers")
	}
	return s.decide(WithApprover(ctx, actor), Approval{
		RequestID: sub.RequestID, SubjectDigest: sub.SubjectDigest, PolicyRevision: sub.PolicyRevision,
		Nonce: sub.Nonce, Decision: sub.Decision, ActorID: actor.ID, ActorKind: actor.Kind,
		Surface: actor.Surface, StepUpAt: actor.StepUpAt, Reason: sub.Reason, Conditions: sub.Conditions,
	}, true)
}

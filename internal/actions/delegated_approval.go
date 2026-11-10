package actions

import (
	"context"
	"encoding/json"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// ApprovalSubject computes the subject digest an approval of a delegated
// action is bound to, from the call's canonical input. It is asked when the
// request is filed and again when the approved call executes, so a target
// that moved in between no longer matches.
type ApprovalSubject func(ctx context.Context, input json.RawMessage) (core.Digest, error)

// DelegatedApproval is a sensitive action whose approval is verified and
// spent by the trusted component that performs it, not by this service. The
// release supervisor is one: it verifies and spends a rollback approval
// itself, so the control zone holds no authority to roll back by its own.
//
// The service files the request under Subject, and before executing checks
// only that the named request is approved for this exact subject and the
// policy in force; the handler receives the approval id and hands it on.
type DelegatedApproval struct {
	Action  core.Action
	Subject ApprovalSubject
}

// RegisterDelegated adds real actions whose approval is spent downstream.
// Each must be sensitive and carry a subject; like Register, it replaces a
// fallback of the same name and refuses a second real registration.
func (r *Registry) RegisterDelegated(delegated ...DelegatedApproval) error {
	batch := make([]core.Action, 0, len(delegated))
	subjects := map[string]ApprovalSubject{}
	for _, d := range delegated {
		if d.Subject == nil || !d.Action.Spec.Sensitivity.NeedsApproval() {
			return invalidf("delegated action %s must be sensitive and name its approval subject", d.Action.Spec.Name)
		}
		batch = append(batch, d.Action)
		subjects[d.Action.Spec.Name] = d.Subject
	}
	return r.add(batch, false, subjects)
}

// subjectFor binds a call: the action's own subject when it is delegated,
// otherwise the digest of the factory, the action and the exact input.
func (s *Service) subjectFor(ctx context.Context, call invocation) (core.Digest, error) {
	if subject := s.cfg.Registry.subjectOf(call.spec.Name); subject != nil {
		digest, err := subject(ctx, call.req.Input)
		if err != nil {
			return "", err
		}
		if err := digest.Validate(); err != nil {
			return "", core.NewError(core.CodeInternal, "the approval subject is malformed").WithCause(err)
		}
		return digest, nil
	}
	return subjectDigest(s.cfg.FactoryID, call.spec.Name, call.req.Input)
}

// checkDelegated refuses a delegated call whose request is not approved for
// this action, subject and policy. It spends nothing: the performing
// component verifies the signed decision and spends it in its own
// transaction, so a replay is refused there.
func (s *Service) checkDelegated(ctx context.Context, call invocation, bound binding) error {
	request, err := s.cfg.Approvals.Get(ctx, call.req.ApprovalID)
	if err != nil {
		return err
	}
	switch {
	case request.FactoryID != s.cfg.FactoryID || request.Action != call.spec.Name || request.SubjectDigest != bound.digest:
		return core.NewError(core.CodeApprovalRequired, "the approval was given for another change")
	case request.PolicyRevision != bound.policy:
		return core.NewError(core.CodeApprovalRequired, "the approval was given under another policy")
	case request.Status == approvals.StatusConsumed:
		return core.NewError(core.CodeReplayed, "the approval was already used")
	case request.Status != approvals.StatusApproved:
		return core.NewError(core.CodeApprovalRequired, "the request is "+string(request.Status)+", not approved")
	case request.Expired(s.cfg.Clock.Now()):
		return core.NewError(core.CodeExpired, "the approval has expired")
	}
	return nil
}

package actions

import (
	"context"
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
)

// authenticated refuses an actor the authentication layer did not fill in.
// The actor comes from a verified session or token, never from the request
// body, so a missing part means the caller was not identified.
func authenticated(a core.Actor) error {
	if a.ID == "" || !a.Kind.Valid() || !a.Surface.Valid() {
		return core.NewError(core.CodeUnauthenticated, "the caller could not be identified")
	}
	return nil
}

// authorize asks the authorizer and applies the rules the service adds on
// top of it. It returns the effect the call proceeds under: allow, or
// require_approval for a call that must spend an approval first.
//
// An authorizer error or an unknown effect denies. A step-up demand from the
// authorizer is passed to the caller. Owner authority is never delegated:
// an action only the Owner may run refuses any actor that is not a person,
// whatever roles it presents, so an agent riding an ambient Owner session
// gets nowhere. A call that needs step-up then spends the person's
// confirmation of exactly this call, once the authorizer has said the
// caller may run the action at all, and the actor carries its time from
// then on.
func (s *Service) authorize(ctx context.Context, call *invocation) (core.AuthzEffect, error) {
	if ownerOnly(call.spec) && call.req.Actor.Kind != core.ActorHuman {
		return "", core.NewError(core.CodePermissionDenied, "only a person acting as the Owner may run "+call.spec.Name)
	}
	if s.requiresStepUpProof(*call) {
		// Asked first without a step-up, so a caller who may not run the
		// action at all is refused as such and spends no confirmation.
		if _, err := s.decide(ctx, *call, true); err != nil {
			return "", err
		}
		if err := s.bindStepUp(ctx, call); err != nil {
			return "", err
		}
	}
	effect, err := s.decide(ctx, *call, false)
	if err != nil {
		return "", err
	}
	if needsStepUp(*call) {
		if err := s.freshStepUp(call.req.Actor); err != nil {
			return "", err
		}
	}
	return effect, nil
}

// decide asks the authorizer about call. With stepUpPending set, a step-up
// demand is not a refusal: the confirmation is spent next.
func (s *Service) decide(ctx context.Context, call invocation, stepUpPending bool) (core.AuthzEffect, error) {
	decision, err := s.cfg.Authorizer.Authorize(ctx, core.AuthzRequest{Actor: call.req.Actor, Action: call.spec})
	if err != nil {
		return "", core.NewError(core.CodePermissionDenied, "the authorization decision could not be made").WithCause(err)
	}
	switch {
	case decision.Effect == core.EffectAllow, decision.Effect == core.EffectRequireApproval:
	case decision.Effect == core.EffectRequireStepUp && stepUpPending:
	case decision.Effect == core.EffectRequireStepUp:
		return "", core.NewError(core.CodeStepUpRequired, "confirm your identity again before running "+call.spec.Name)
	default:
		return "", core.NewError(core.CodePermissionDenied, fmt.Sprintf("the actor may not run %s", call.spec.Name))
	}
	return decision.Effect, nil
}

// needsStepUp reports whether this call must carry a fresh confirmation.
// Asking for a dry run or filing a plan acts on nothing, so only the call
// that executes, or records a decision, needs one.
func needsStepUp(call invocation) bool {
	if !call.spec.StepUp || call.req.DryRun {
		return false
	}
	return call.spec.Name == ApprovalDecisionAction || call.req.ApprovalID != ""
}

// freshStepUp requires a person who confirmed their identity within the
// step-up age. An agent or a service never satisfies it.
func (s *Service) freshStepUp(a core.Actor) error {
	now := s.cfg.Clock.Now()
	if a.Kind != core.ActorHuman || a.StepUpAt.IsZero() || a.StepUpAt.After(now) || now.Sub(a.StepUpAt) > s.cfg.StepUpMaxAge {
		return core.NewError(core.CodeStepUpRequired, "this action needs a person who has just confirmed their identity")
	}
	return nil
}

// ownerOnly reports whether the Owner is the only role the action names.
func ownerOnly(spec core.ActionSpec) bool {
	for _, role := range spec.Roles {
		if role != core.RoleOwner {
			return false
		}
	}
	return len(spec.Roles) > 0
}

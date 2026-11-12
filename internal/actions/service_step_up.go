package actions

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// StepUpProofs spends a person's confirmation of one call; *auth.Service is
// one. A confirmation is made at the sign-in provider for exactly one
// action and target, and authorizes one call.
type StepUpProofs interface {
	ConsumeStepUp(ctx context.Context, sessionID, action, target string) (time.Time, error)
}

// Details a STEP_UP_REQUIRED answer carries, so the dashboard can start the
// confirmation of exactly the refused call.
const (
	DetailStepUpAction = "step_up_action"
	DetailStepUpTarget = "step_up_target"
)

// requiresStepUpProof reports whether the call must spend a confirmation:
// every call of an action that needs step-up, except a dry run, which acts
// on nothing, and a fallback, which only reports that the action is not
// wired here.
func (s *Service) requiresStepUpProof(call invocation) bool {
	return call.spec.StepUp && !call.req.DryRun && !s.cfg.Registry.IsFallback(call.spec.Name)
}

// DecisionStepUpTarget is what a step-up for deciding the approval request
// requestID with decision names: the request and the verdict, so a
// confirmation made to reject can never approve.
func DecisionStepUpTarget(requestID string, decision approvals.Decision) string {
	return requestID + "/" + string(decision)
}

// stepUpTarget is what the confirmation of call must name: the approval
// request a decision answers with its verdict, the approval an execution
// spends, or, for a plan, the digest of the exact canonical input.
func (s *Service) stepUpTarget(call invocation) (string, error) {
	switch {
	case call.spec.Name == ApprovalDecisionAction:
		var in struct {
			RequestID string             `json:"request_id"`
			Decision  approvals.Decision `json:"decision"`
		}
		if err := json.Unmarshal(call.req.Input, &in); err != nil || in.RequestID == "" || !in.Decision.Valid() {
			return "", core.NewError(core.CodeInvalidArgument, "a decision names the request_id it answers and its decision")
		}
		return DecisionStepUpTarget(in.RequestID, in.Decision), nil
	case call.req.ApprovalID != "":
		return call.req.ApprovalID, nil
	}
	digest, err := subjectDigest(s.cfg.FactoryID, call.spec.Name, call.req.Input)
	return string(digest), err
}

// bindStepUp spends the confirmation the person made for exactly this call
// and stamps its time on the actor, which is the only way an actor gets a
// step-up time. It is accepted only from the dashboard's own form posts in
// a person's browser session: a token never carries one, and neither does
// the session when it is used through the REST surface, which is what an
// agent in the page, such as a WebMCP tool, calls.
func (s *Service) bindStepUp(ctx context.Context, call *invocation) error {
	target, err := s.stepUpTarget(*call)
	if err != nil {
		return err
	}
	a := call.req.Actor
	if a.Kind != core.ActorHuman || a.Surface != core.SurfaceDashboard || a.SessionID == "" || s.cfg.StepUp == nil {
		return stepUpRefusal(call.spec.Name, target,
			call.spec.Name+" runs only from the dashboard, after you confirm your identity there for this exact call")
	}
	at, err := s.cfg.StepUp.ConsumeStepUp(ctx, a.SessionID, call.spec.Name, target)
	if err != nil {
		return stepUpRefusal(call.spec.Name, target,
			"confirm your identity for this exact call first; each confirmation authorizes one call")
	}
	call.req.Actor.StepUpAt = at
	return nil
}

// stepUpRefusal is STEP_UP_REQUIRED naming the call to confirm.
func stepUpRefusal(action, target, message string) error {
	e := core.NewError(core.CodeStepUpRequired, message)
	e.Details = map[string]string{DetailStepUpAction: action, DetailStepUpTarget: target}
	return e
}

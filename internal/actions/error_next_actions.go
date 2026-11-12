package actions

import (
	"errors"
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
)

// statusAction is where a caller with nothing better to do looks next.
const statusAction = "factory.status"

// unknown answers a name the registry does not hold, pointing at the
// registered actions of the same area.
func (s *Service) unknown(name string) error {
	var next []core.NextAction
	for _, n := range s.cfg.Registry.sameArea(name, 3) {
		next = append(next, core.NextAction{Action: n, Description: "a registered action of the same area"})
	}
	if len(next) == 0 && s.cfg.Registry.has(statusAction) {
		next = append(next, core.NextAction{Action: statusAction, Description: "see what the factory offers"})
	}
	e := core.NewError(core.CodeNotFound, fmt.Sprintf("no action is named %q", clip(name, 80)), next...)
	if len(next) == 0 {
		// An empty registry has nothing to point at; the error still says so.
		e.NextActions = []core.NextAction{{Action: name, Description: "no action of this name is registered"}}
	}
	return e
}

// withNextActions makes every failure a *core.Error that carries at least
// one next action. An error without a code becomes INTERNAL with a fixed
// message, the original kept as its cause for the operator, so a driver or
// file-system message never reaches the caller. A typed error keeps its code
// and message; one without next actions gets the step its code suggests.
func (s *Service) withNextActions(spec core.ActionSpec, err error) error {
	var typed *core.Error
	if !errors.As(err, &typed) {
		return core.NewError(core.CodeInternal, spec.Name+" failed; see the server log",
			s.registered(spec, statusAction, "check the health of the factory")...).WithCause(err)
	}
	if len(typed.NextActions) > 0 {
		return typed
	}
	out := core.NewError(typed.Code, typed.Message).WithCause(err)
	out.Details = typed.Details
	out.NextActions = s.defaultNext(spec, typed.Code)
	return out
}

// defaultNext is the step a caller may take after a failure of code.
func (s *Service) defaultNext(spec core.ActionSpec, code core.ErrorCode) []core.NextAction {
	switch code {
	case core.CodeInvalidArgument:
		return s.registered(spec, spec.Name, "fix the input against the action's input schema and call again")
	case core.CodeStepUpRequired:
		next := s.registered(spec, spec.Name, "confirm your identity again, then call again within the step-up window")
		if s.cfg.StepUpPage != "" {
			next[0].Description = "a token never carries a step-up: sign in to the dashboard, confirm your identity there and decide in " + s.cfg.StepUpPage
			next[0].Params = map[string]string{"dashboard": s.cfg.StepUpPage}
		}
		return next
	case core.CodeApprovalRequired, core.CodeReplayed, core.CodeExpired:
		return s.registered(spec, spec.Name, "call again without approval_id to plan this exact input")
	case core.CodePermissionDenied, core.CodeUnauthenticated:
		return s.registered(spec, statusAction, "ask the Owner for a role that may run "+spec.Name)
	case core.CodeConflict, core.CodeNotFound:
		return s.registered(spec, spec.Name, "read the current state and call again")
	case core.CodeNotSupported:
		return s.registered(spec, statusAction, "this factory cannot run "+spec.Name+" yet")
	case core.CodeUnavailable, core.CodeTimeout, core.CodeRateLimited:
		return s.registered(spec, spec.Name, "call again later")
	}
	return s.registered(spec, statusAction, "check the health of the factory")
}

// registered returns a next action naming want when it is registered, and
// the failed action itself otherwise, so a next action never names an
// action that does not exist.
func (s *Service) registered(spec core.ActionSpec, want, description string) []core.NextAction {
	if !s.cfg.Registry.has(want) {
		want = spec.Name
	}
	return []core.NextAction{{Action: want, Description: description}}
}

// messageOf returns the caller-safe message of a typed error.
func messageOf(err error) string {
	var typed *core.Error
	if errors.As(err, &typed) {
		return typed.Message
	}
	return "the request failed"
}

func clip(text string, n int) string {
	if len(text) <= n {
		return text
	}
	return text[:n] + "..."
}

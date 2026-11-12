package auth

import (
	"context"
	"regexp"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// The actions a step-up names to mint a personal access token. Each is
// bound to one target like every other step-up: TokenStepUpAction to the
// digest of the exact token request (see PATRequest.StepUpTarget), and
// DeviceApprovalStepUpAction to the user code of the one device login
// request it approves. Neither is ever spent by the action service.
const (
	TokenStepUpAction          = "auth.tokens.create"
	DeviceApprovalStepUpAction = "auth.device.approve"
)

// maxProofsPerSession bounds the confirmations one session may hold at once;
// the oldest goes first.
const maxProofsPerSession = 16

// stepUpActionPattern and stepUpTargetPattern bound what a step-up may be
// bound to: an action name, and an approval request ID or a digest.
var (
	stepUpActionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	stepUpTargetPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,255}$`)
)

// StepUpTarget is what one step-up confirms: an action, and the one request,
// approval or canonical input digest it may be used for.
type StepUpTarget struct {
	Action string
	Target string
}

// stepUpProof is one confirmation a person made at the provider. It is
// spent by the one call it names, and lapses after StepUpMaxAge.
type stepUpProof struct {
	confirmedAt time.Time
	expires     time.Time
}

// parseStepUpTarget validates the target a step-up is started for. Every
// step-up names an action and the one target it confirms; there is no
// generic confirmation.
func parseStepUpTarget(action, target string) (StepUpTarget, error) {
	switch {
	case !stepUpActionPattern.MatchString(action) || len(action) > 128:
		return StepUpTarget{}, invalidArgument("a step-up names the action it confirms")
	case !stepUpTargetPattern.MatchString(target):
		return StepUpTarget{}, invalidArgument("a step-up names the one request, approval or input digest it confirms")
	}
	return StepUpTarget{Action: action, Target: target}, nil
}

// addProofLocked records a confirmation of target on ss at now, replacing
// an earlier one for the same target and dropping lapsed ones.
func (s *Service) addProofLocked(ss *session, target StepUpTarget, now time.Time) {
	if ss.proofs == nil {
		ss.proofs = map[StepUpTarget]stepUpProof{}
	}
	for k, p := range ss.proofs {
		if !now.Before(p.expires) {
			delete(ss.proofs, k)
		}
	}
	for len(ss.proofs) >= maxProofsPerSession {
		var oldest StepUpTarget
		var at time.Time
		for k, p := range ss.proofs {
			if at.IsZero() || p.confirmedAt.Before(at) {
				oldest, at = k, p.confirmedAt
			}
		}
		delete(ss.proofs, oldest)
	}
	ss.proofs[target] = stepUpProof{confirmedAt: now, expires: now.Add(s.cfg.StepUpMaxAge)}
}

// consumeProofLocked spends the confirmation of target on ss and returns
// when it was made. A missing, lapsed or other target's confirmation is
// STEP_UP_REQUIRED.
func (s *Service) consumeProofLocked(ss *session, target StepUpTarget, now time.Time) (time.Time, error) {
	p, ok := ss.proofs[target]
	delete(ss.proofs, target)
	if !ok || !s.freshStepUp(p.confirmedAt, now) || !now.Before(p.expires) {
		return time.Time{}, core.NewError(core.CodeStepUpRequired,
			"confirm your identity for exactly this "+target.Action+" call first; a confirmation is spent by one call")
	}
	return p.confirmedAt, nil
}

// ConsumeStepUp spends the confirmation the session with the public handle
// holds for action on target, and returns when the person made it. Each
// confirmation authorizes one call: a second call, another target or
// another action needs a new one. A token's handle never holds one, and a
// confirmation to mint a token is spent only by minting it.
func (s *Service) ConsumeStepUp(_ context.Context, handle, action, target string) (time.Time, error) {
	want, err := parseStepUpTarget(action, target)
	if err != nil || mintsToken(want.Action) {
		return time.Time{}, core.NewError(core.CodeStepUpRequired, "a step-up confirms one named call")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.byHandle[handle]
	if ok {
		ss, ok = s.liveSessionLocked(ss.key)
	}
	if !ok {
		return time.Time{}, core.NewError(core.CodeStepUpRequired, "a step-up is made in a live browser session")
	}
	return s.consumeProofLocked(ss, want, s.cfg.Clock.Now())
}

// mintsToken reports whether action is one of the confirmations only token
// minting spends.
func mintsToken(action string) bool {
	return action == TokenStepUpAction || action == DeviceApprovalStepUpAction
}

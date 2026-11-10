package statemachine

import (
	"fmt"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
)

// The rules below judge a proof against the requested transition. They are
// pure: the same proof at the same instant always gets the same answer, and
// the zero value of every proof is refused.

// refuse builds the GUARD_FAILED error of one guard. The details name the
// guard and the step so a caller can tell which proof is missing.
func refuse(guard GuardKind, t core.Transition, format string, args ...any) *core.Error {
	refusal := core.NewError(core.CodeGuardFailed, fmt.Sprintf(format, args...))
	refusal.Details = map[string]string{
		"guard": string(guard),
		"from":  string(t.From),
		"event": string(t.Event),
		"to":    string(t.To),
	}
	return refusal
}

// checkClaim requires the claim to be written under the live lease presented
// and to carry a held budget reservation for the same issue and run.
func checkClaim(t core.Transition, p ClaimProof, now time.Time) error {
	res := p.Reservation
	switch {
	case t.RunID == "":
		return refuse(GuardClaim, t, "a claim must name the run it starts")
	case t.LeaseOwner == "" || t.FencingToken <= 0:
		return refuse(GuardClaim, t, "a claim must be written under a lease with a fencing token")
	case p.LeaseOwner != t.LeaseOwner || p.FencingToken != t.FencingToken:
		return refuse(GuardClaim, t, "the lease presented is not the one this claim is written under")
	case !now.Before(p.LeaseExpiresAt):
		return refuse(GuardClaim, t, "the lease on the issue has expired")
	case res.ID == "":
		return refuse(GuardClaim, t, "a claim needs a budget reservation")
	case res.Status != core.ReservationHeld:
		return refuse(GuardClaim, t, "the budget reservation is %q, not held", string(res.Status))
	case res.IssueID != t.IssueID || res.RunID != t.RunID:
		return refuse(GuardClaim, t, "the budget reservation belongs to another issue or run")
	case !res.Mode.Valid():
		return refuse(GuardClaim, t, "the budget reservation does not state its budget mode")
	case res.Amount < 0:
		return refuse(GuardClaim, t, "the budget reservation has a negative amount")
	}
	return nil
}

// checkQAPass requires a complete, unexpired receipt in which every required
// hard gate passed and no hard gate failed, and whose score reaches the pass
// score. The gates are judged before the score, so a high score never buys
// past a failed gate.
func checkQAPass(t core.Transition, p QAProof, now time.Time) error {
	receipt := p.Receipt
	if err := receipt.Validate(); err != nil {
		return refuse(GuardQAPass, t, "the QA receipt is not valid: %v", err).WithCause(err)
	}
	switch {
	case receipt.Expired(now):
		return refuse(GuardQAPass, t, "the QA receipt has expired")
	case len(p.RequiredGates) == 0:
		return refuse(GuardQAPass, t, "the review criteria name no required hard gates")
	case p.PassScore < 1 || p.PassScore > 100:
		return refuse(GuardQAPass, t, "the review criteria state no pass score within 1-100")
	case !receipt.HardGatesPassed():
		return refuse(GuardQAPass, t, "a hard gate failed or none was recorded")
	}
	passed := map[string]bool{}
	for _, check := range receipt.Checks {
		if check.Kind == core.CheckHardGate && check.Passed {
			passed[check.Name] = true
		}
	}
	for _, gate := range p.RequiredGates {
		if gate == "" || !passed[gate] {
			refusal := refuse(GuardQAPass, t, "required hard gate %q has no passing result", gate)
			refusal.Details["gate"] = gate
			return refusal
		}
	}
	if receipt.Score < p.PassScore {
		return refuse(GuardQAPass, t, "QA score %d is below the pass score %d", receipt.Score, p.PassScore)
	}
	return nil
}

// checkPostDeploy requires the released digest to be the one observed
// running and healthy, the observation to date from after the observation
// window closed, and every item of the Definition of Done to be met.
func checkPostDeploy(t core.Transition, p PostDeployProof, now time.Time) error {
	switch {
	case p.Released.Validate() != nil:
		return refuse(GuardPostDeploy, t, "the release is not identified by a full digest")
	case p.Observed != p.Released:
		return refuse(GuardPostDeploy, t, "the revision observed running is not the released one")
	case !p.Healthy:
		return refuse(GuardPostDeploy, t, "the running revision is not healthy")
	case p.WindowEndsAt.IsZero() || now.Before(p.WindowEndsAt):
		return refuse(GuardPostDeploy, t, "the observation window has not passed")
	case p.ObservedAt.Before(p.WindowEndsAt):
		return refuse(GuardPostDeploy, t, "the revision was not observed after the observation window closed")
	case len(p.DoD) == 0:
		return refuse(GuardPostDeploy, t, "no Definition of Done result was recorded")
	}
	for _, result := range p.DoD {
		item := strings.TrimSpace(result.Item)
		if item == "" {
			return refuse(GuardPostDeploy, t, "a Definition of Done result names no item")
		}
		if !result.Met {
			refusal := refuse(GuardPostDeploy, t, "Definition of Done item %q is not met", item)
			refusal.Details["dod_item"] = item
			return refusal
		}
	}
	return nil
}

// checkRetry requires the retry to name its actor and that actor to be
// allowed outright. A decision that still needs an approval or a step-up is
// not an authorization.
func checkRetry(t core.Transition, decision core.AuthzDecision) error {
	if strings.TrimSpace(t.ActorID) == "" {
		return refuse(GuardRetry, t, "a retry must name the actor who asked for it")
	}
	if !decision.Allowed() {
		refusal := refuse(GuardRetry, t, "the retry is not authorized")
		refusal.Details["effect"] = string(decision.Effect)
		if decision.Reason != "" {
			refusal.Details["reason"] = decision.Reason
		}
		return refusal
	}
	return nil
}

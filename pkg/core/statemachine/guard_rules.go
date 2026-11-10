package statemachine

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
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

// refuseAs restates the refusal of a shared rule as the refusal of a guard,
// keeping the rule's message and details. An error that is not a typed
// refusal says nothing a caller may see, so it is kept only as the cause.
func refuseAs(guard GuardKind, t core.Transition, err error) *core.Error {
	var rule *core.Error
	if !errors.As(err, &rule) {
		return refuse(guard, t, "the %s rule does not hold", string(guard)).WithCause(err)
	}
	refusal := refuse(guard, t, "%s", rule.Message)
	maps.Copy(refusal.Details, rule.Details)
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

// checkAttempts requires a counted run with a stated limit that it has not
// reached, and a retry that carries exactly the next attempt number.
func checkAttempts(t core.Transition, p AttemptsProof) error {
	switch {
	case t.RunID == "":
		return refuse(GuardAttempts, t, "a retry must name the run it continues")
	case p.IssueID != t.IssueID || p.RunID != t.RunID:
		return refuse(GuardAttempts, t, "the attempt count belongs to another issue or run")
	case p.MaxAttempts < 1:
		return refuse(GuardAttempts, t, "policy states no attempt limit for the run")
	case p.Attempt < 1:
		return refuse(GuardAttempts, t, "the run has no counted attempt")
	}
	if p.Attempt >= p.MaxAttempts {
		refusal := refuse(GuardAttempts, t, "the run has used %d of its %d attempts", p.Attempt, p.MaxAttempts)
		refusal.Details["attempt"] = strconv.Itoa(p.Attempt)
		refusal.Details["max_attempts"] = strconv.Itoa(p.MaxAttempts)
		return refusal
	}
	if t.Attempt != p.Attempt+1 {
		return refuse(GuardAttempts, t, "a retry after attempt %d must be attempt %d, not %d", p.Attempt, p.Attempt+1, t.Attempt)
	}
	return nil
}

// checkQAPass requires a complete receipt for the issue, the run and the
// candidate of the transition, issued in the past and unexpired, that passes
// the shared pass rule under criteria that demand every required hard gate.
func checkQAPass(t core.Transition, p QAProof, now time.Time) error {
	receipt := p.Receipt
	if err := receipt.Validate(); err != nil {
		return refuse(GuardQAPass, t, "the QA receipt is not valid: %v", err).WithCause(err)
	}
	switch {
	case t.RunID == "" || t.CandidateDigest == "":
		return refuse(GuardQAPass, t, "the step must name the run and the candidate digest it releases")
	case receipt.IssueID != t.IssueID || receipt.RunID != t.RunID:
		return refuse(GuardQAPass, t, "the QA receipt belongs to another issue or run")
	case receipt.CandidateDigest != t.CandidateDigest:
		return refuse(GuardQAPass, t, "the QA receipt is for another candidate")
	case receipt.IssuedAt.After(now):
		return refuse(GuardQAPass, t, "the QA receipt is dated after the current instant")
	case receipt.Expired(now):
		return refuse(GuardQAPass, t, "the QA receipt has expired")
	}
	for _, gate := range core.RequiredHardGates() {
		if !slices.Contains(p.RequiredGates, gate) {
			refusal := refuse(GuardQAPass, t, "the review criteria do not demand required hard gate %q", gate)
			refusal.Details["gate"] = gate
			return refusal
		}
	}
	if err := receipt.CheckPass(p.PassScore, p.RequiredGates...); err != nil {
		return refuseAs(GuardQAPass, t, err)
	}
	return nil
}

// checkPostDeploy requires the observation to be of the issue, the run and
// the release of the transition, the released digest to be the one observed
// running and healthy, the observation to date from after the observation
// window closed and not from after now, and every item of the Definition of
// Done to be met.
func checkPostDeploy(t core.Transition, p PostDeployProof, now time.Time) error {
	switch {
	case t.RunID == "" || t.ReleaseDigest == "":
		return refuse(GuardPostDeploy, t, "the step must name the run and the release digest it verifies")
	case p.IssueID != t.IssueID || p.RunID != t.RunID:
		return refuse(GuardPostDeploy, t, "the observation belongs to another issue or run")
	case p.Released.Validate() != nil:
		return refuse(GuardPostDeploy, t, "the release is not identified by a full digest")
	case p.Released != t.ReleaseDigest:
		return refuse(GuardPostDeploy, t, "the observation is of another release")
	case p.Observed != p.Released:
		return refuse(GuardPostDeploy, t, "the revision observed running is not the released one")
	case !p.Healthy:
		return refuse(GuardPostDeploy, t, "the running revision is not healthy")
	case p.WindowEndsAt.IsZero() || now.Before(p.WindowEndsAt):
		return refuse(GuardPostDeploy, t, "the observation window has not passed")
	case p.ObservedAt.Before(p.WindowEndsAt):
		return refuse(GuardPostDeploy, t, "the revision was not observed after the observation window closed")
	case p.ObservedAt.After(now):
		return refuse(GuardPostDeploy, t, "the observation is dated after the current instant")
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

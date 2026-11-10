package statemachine

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// retryDecided is the proof of a retry after a rollback on which the
// authorization decision was the given one.
func retryDecided(decision core.AuthzDecision) func(core.Transition) RetryProof {
	return func(t core.Transition) RetryProof {
		proof := validRetry(t)
		proof.Decision = decision
		return proof
	}
}

// checkRetryWith runs the step from Blocked back to Ready with the given
// authorization decision, after mutate has changed the request.
func checkRetryWith(decision core.AuthzDecision, mutate func(*core.Transition)) error {
	request := retryRequest()
	mutate(&request)
	return machineWith(Guards{Retry: retryWith(retryDecided(decision))}).Check(context.Background(), request)
}

// checkRetryProof runs the step from Blocked back to Ready on the proof base
// builds, after mutate has changed it.
func checkRetryProof(base func(core.Transition) RetryProof, mutate func(*RetryProof)) error {
	request := retryRequest()
	proof := base(request)
	mutate(&proof)
	guards := Guards{Retry: retryWith(func(core.Transition) RetryProof { return proof })}
	return machineWith(guards).Check(context.Background(), request)
}

func unchanged(*core.Transition) {}

func TestABlockedIssueReturnsToReadyOnAnAuthorizedRetry(t *testing.T) {
	if err := checkRetryWith(allowed(), unchanged); err != nil {
		t.Fatalf("an authorized retry was refused: %v", err)
	}
}

func TestARetryThatIsNotAuthorizedLeavesTheIssueBlocked(t *testing.T) {
	cases := map[string]core.AuthzDecision{
		"no decision was made":          {},
		"the actor is denied":           {Effect: core.EffectDeny, Reason: "reporters cannot retry"},
		"an approval is still required": {Effect: core.EffectRequireApproval},
		"a step-up is still required":   {Effect: core.EffectRequireStepUp},
		"the decision is not a known effect": {
			Effect: "probably",
		},
	}
	for name, decision := range cases {
		requireGuardFailed(t, name, checkRetryWith(decision, unchanged), GuardRetry)
	}
}

func TestARetryMustNameWhoAskedForIt(t *testing.T) {
	err := checkRetryWith(allowed(), func(r *core.Transition) { r.ActorID = "" })
	requireGuardFailed(t, "anonymous retry", err, GuardRetry)

	blank := checkRetryWith(allowed(), func(r *core.Transition) { r.ActorID = "  " })
	requireGuardFailed(t, "blank actor", blank, GuardRetry)
}

func TestARefusedRetryReportsTheAuthorizationOutcome(t *testing.T) {
	decision := core.AuthzDecision{Effect: core.EffectRequireApproval, Reason: "owner approval pending"}
	details := detailsOf(checkRetryWith(decision, unchanged))
	if details["effect"] != string(core.EffectRequireApproval) || details["reason"] != "owner approval pending" {
		t.Fatalf("refusal details = %v, want the authorization effect and reason", details)
	}
}

func TestOnlyBlockedIssuesCanBeRetried(t *testing.T) {
	for _, from := range core.States() {
		if from == core.StateBlocked {
			continue
		}
		request := transitionOn(from, core.EventRetryAuthorized, core.StateReady)
		err := machineWith(passingGuards()).Check(context.Background(), request)
		if !core.HasCode(err, core.CodeInvalidTransition) {
			t.Errorf("an issue in %s was retried: %v", from, err)
		}
	}
}

func TestAnEmptyRetryProofIsRefused(t *testing.T) {
	t.Parallel()
	guards := Guards{Retry: retryWith(func(core.Transition) RetryProof { return RetryProof{} })}
	err := machineWith(guards).Check(context.Background(), retryRequest())
	requireGuardFailed(t, "empty proof", err, GuardRetry)
}

func TestARetryMustBeAuthorizedForTheActorAndTheIssueThatAskForIt(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*RetryProof){
		"the retry was judged for another issue": func(p *RetryProof) { p.IssueID = otherIssue },
		"the retry was judged for no issue":      func(p *RetryProof) { p.IssueID = core.IssueRef{} },
		"another actor was authorized":           func(p *RetryProof) { p.Actor.ID = "user:maintainer-2" },
		"no actor was authenticated":             func(p *RetryProof) { p.Actor = core.Actor{} },
		"the kind of the actor is unknown":       func(p *RetryProof) { p.Actor.Kind = "" },
		"the kind of the actor is not a kind":    func(p *RetryProof) { p.Actor.Kind = "bot" },
		"the cause of the block is unknown":      func(p *RetryProof) { p.Cause = "" },
		"the cause of the block is not a cause":  func(p *RetryProof) { p.Cause = "stuck" },
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkRetryProof(validRetry, mutate), GuardRetry)
	}
}

func TestAnAgentCannotRetryABlockedIssue(t *testing.T) {
	t.Parallel()
	// An allow decision does not help: the coding agent whose release was
	// rolled back, or whose scope was refused, never sends its own issue back.
	agent := func(p *RetryProof) { p.Actor.Kind = core.ActorAgent }
	requireGuardFailed(t, "agent retries after a rollback", checkRetryProof(validRetry, agent), GuardRetry)
	requireGuardFailed(t, "agent retries after a denial with a fresh approval",
		checkRetryProof(retryAfterDenial, agent), GuardRetry)

	service := checkRetryProof(validRetry, func(p *RetryProof) { p.Actor.Kind = core.ActorService })
	if service != nil {
		t.Fatalf("an authorized retry by a service after a rollback was refused: %v", service)
	}
}

func TestARetryCannotUndoADeniedOrExpiredApprovalWithoutAScopeProof(t *testing.T) {
	t.Parallel()
	for _, cause := range []BlockCause{BlockApprovalDenied, BlockApprovalExpired} {
		bare := checkRetryProof(validRetry, func(p *RetryProof) { p.Cause = cause })
		requireGuardFailed(t, "bare allow after "+string(cause), bare, GuardRetry)
		if got := detailsOf(bare)["cause"]; got != string(cause) {
			t.Errorf("%s: refusal reports cause %q", cause, got)
		}
	}
	if !BlockApprovalDenied.NeedsScopeProof() || !BlockApprovalExpired.NeedsScopeProof() ||
		BlockRolledBack.NeedsScopeProof() || BlockCause("").NeedsScopeProof() {
		t.Fatal("only a block over an approval needs the scope proven again")
	}
}

func TestARetryAfterADenialNeedsAnApprovalGivenAfterTheBlock(t *testing.T) {
	t.Parallel()
	if err := checkRetryProof(retryAfterDenial, func(*RetryProof) {}); err != nil {
		t.Fatalf("a retry with an approval the Owner gave after the block was refused: %v", err)
	}
	cases := map[string]func(*RetryProof){
		"the approval is the rejection that blocked the issue": func(p *RetryProof) {
			p.Approval.Request.Status = approvals.StatusRejected
			p.Approval.Approval.Decision = approvals.DecisionReject
		},
		"the approval has expired": func(p *RetryProof) { p.Approval.Approval.ExpiresAt = testNow },
		"the approval was already used": func(p *RetryProof) {
			p.Approval.Request.Status = approvals.StatusConsumed
		},
		"the approval was decided before the block": func(p *RetryProof) {
			p.BlockedAt = p.Approval.Approval.DecidedAt.Add(time.Minute)
		},
		"the approval was decided at the instant of the block": func(p *RetryProof) {
			p.BlockedAt = p.Approval.Approval.DecidedAt
		},
		"the approval is for another scope": func(p *RetryProof) { p.Approval.ScopeDigest = testDigest },
		"the approval is for another issue": func(p *RetryProof) { p.Approval.Request.IssueID = otherIssue },
		"the approval is for another action": func(p *RetryProof) {
			p.Approval.Request.Action, p.Approval.Approval.Action = "release.publish", "release.publish"
		},
		"an agent gave the approval": func(p *RetryProof) { p.Approval.Approval.ActorKind = core.ActorAgent },
		"the approval is empty":      func(p *RetryProof) { p.Approval = &ApprovalProof{} },
		"it is not known when the issue was blocked": func(p *RetryProof) {
			p.BlockedAt = time.Time{}
		},
		"the block is dated after now": func(p *RetryProof) { p.BlockedAt = testNow.Add(time.Nanosecond) },
		"a failing approval is not rescued by a delegation": func(p *RetryProof) {
			p.Approval.Request.Status = approvals.StatusRejected
			delegation := validDelegation(retryRequest())
			p.Delegation = &delegation
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkRetryProof(retryAfterDenial, mutate), GuardRetry)
	}
}

func TestARetryAfterADenialMayRestOnTheOwnersDelegation(t *testing.T) {
	t.Parallel()
	delegated := func(t core.Transition) RetryProof {
		proof := validRetry(t)
		proof.Cause = BlockApprovalExpired
		delegation := validDelegation(t)
		proof.Delegation = &delegation
		return proof
	}
	if err := checkRetryProof(delegated, func(*RetryProof) {}); err != nil {
		t.Fatalf("a retry of a scope that now lies within the delegation was refused: %v", err)
	}
	cases := map[string]func(*RetryProof){
		"policy still wants the Owner": func(p *RetryProof) {
			p.Delegation.Decision = core.AuthzDecision{Effect: core.EffectRequireApproval}
		},
		"the delegation was judged for another issue": func(p *RetryProof) { p.Delegation.IssueID = otherIssue },
		"the delegation is empty":                     func(p *RetryProof) { p.Delegation = &DelegationProof{} },
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkRetryProof(delegated, mutate), GuardRetry)
	}
}

func TestARetryAfterARollbackNeedsNoScopeProof(t *testing.T) {
	t.Parallel()
	// The scope of a rolled-back issue was approved or delegated before it
	// was ever claimed; the retry is held to its authorization alone.
	err := checkRetryProof(validRetry, func(p *RetryProof) { p.BlockedAt = time.Time{} })
	if err != nil {
		t.Fatalf("an authorized retry after a rollback was refused: %v", err)
	}
}

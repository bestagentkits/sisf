package statemachine

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// checkDelegationWith runs the step from ScopeDraft straight to Ready after
// mutate has changed the proof.
func checkDelegationWith(mutate func(*DelegationProof)) error {
	request := delegatedScopeRequest()
	proof := validDelegation(request)
	mutate(&proof)
	guards := Guards{Delegation: delegationWith(func(core.Transition) DelegationProof { return proof })}
	return machineWith(guards).Check(context.Background(), request)
}

func TestAScopeWithinTheOwnersDelegationBecomesReadyWithoutAnApproval(t *testing.T) {
	t.Parallel()
	if err := checkDelegationWith(func(*DelegationProof) {}); err != nil {
		t.Fatalf("a scope that policy permits was refused: %v", err)
	}
}

func TestAScopeOutsideTheOwnersDelegationDoesNotBecomeReady(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*DelegationProof){
		"policy made no decision":       func(p *DelegationProof) { p.Decision = core.AuthzDecision{} },
		"policy denies the scope":       func(p *DelegationProof) { p.Decision = core.AuthzDecision{Effect: core.EffectDeny} },
		"the scope needs an approval":   func(p *DelegationProof) { p.Decision = core.AuthzDecision{Effect: core.EffectRequireApproval} },
		"the scope needs a step-up":     func(p *DelegationProof) { p.Decision = core.AuthzDecision{Effect: core.EffectRequireStepUp} },
		"the decision is unknown":       func(p *DelegationProof) { p.Decision = core.AuthzDecision{Effect: "probably"} },
		"no scope was judged":           func(p *DelegationProof) { p.ScopeDigest = "" },
		"the scope is not a digest":     func(p *DelegationProof) { p.ScopeDigest = "sow-v2" },
		"the policy is not pinned":      func(p *DelegationProof) { p.PolicyRevision = "" },
		"the policy is a branch":        func(p *DelegationProof) { p.PolicyRevision = "main" },
		"the scope is of another issue": func(p *DelegationProof) { p.IssueID = otherIssue },
		"the proof names no issue":      func(p *DelegationProof) { p.IssueID = core.IssueRef{} },
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkDelegationWith(mutate), GuardDelegation)
	}
}

func TestAnEmptyDelegationProofIsRefused(t *testing.T) {
	t.Parallel()
	guards := Guards{Delegation: delegationWith(func(core.Transition) DelegationProof { return DelegationProof{} })}
	err := machineWith(guards).Check(context.Background(), delegatedScopeRequest())
	requireGuardFailed(t, "empty proof", err, GuardDelegation)
}

func TestAScopeThatNeedsTheOwnerIsPointedToTheApprovalStep(t *testing.T) {
	t.Parallel()
	err := checkDelegationWith(func(p *DelegationProof) {
		p.Decision = core.AuthzDecision{Effect: core.EffectRequireApproval, Reason: "new feature"}
	})
	details := detailsOf(err)
	if details["effect"] != string(core.EffectRequireApproval) || details["reason"] != "new feature" {
		t.Errorf("refusal details = %v, want the policy's effect and reason", details)
	}
	if details["allowed_event"] != string(core.EventApprovalRequired) {
		t.Errorf("refusal names %q as the way on, want %s", details["allowed_event"], core.EventApprovalRequired)
	}
}

func TestWithoutDelegationProofTheOnlyWayOutOfScopeDraftIsTheApprovalRequest(t *testing.T) {
	t.Parallel()
	machine := machineWith(Guards{})
	requireGuardFailed(t, "no delegation guard",
		machine.Check(context.Background(), delegatedScopeRequest()), GuardDelegation)

	toApproval := transitionOn(core.StateScopeDraft, core.EventApprovalRequired, core.StateApprovalPending)
	if err := machine.Check(context.Background(), toApproval); err != nil {
		t.Fatalf("asking the Owner for approval was refused: %v", err)
	}
	for _, e := range Edges() {
		if e.From == core.StateScopeDraft && e.Guard == GuardNone && e.To != core.StateApprovalPending {
			t.Errorf("ScopeDraft has an unguarded way to %s on %s", e.To, e.Event)
		}
	}
}

func TestAnOwnerApprovalDoesNotStandInForDelegationProof(t *testing.T) {
	t.Parallel()
	// The straight step to Ready is for scopes that need no approval. An
	// approval, however valid, belongs to the step out of ApprovalPending.
	guards := Guards{Approval: approvalWith(validApproval)}
	err := machineWith(guards).Check(context.Background(), delegatedScopeRequest())
	requireGuardFailed(t, "approval offered on the delegated step", err, GuardDelegation)
}

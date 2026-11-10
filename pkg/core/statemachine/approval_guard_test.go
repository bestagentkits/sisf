package statemachine

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// checkApprovalWith runs the step from ApprovalPending to Ready after mutate
// has changed the proof.
func checkApprovalWith(mutate func(*ApprovalProof)) error {
	request := ownerApprovedRequest()
	proof := validApproval(request)
	mutate(&proof)
	guards := Guards{Approval: approvalWith(func(core.Transition) ApprovalProof { return proof })}
	return machineWith(guards).Check(context.Background(), request)
}

func TestAnIssueBecomesReadyOnAValidUnusedOwnerApprovalOfItsScope(t *testing.T) {
	t.Parallel()
	if err := checkApprovalWith(func(*ApprovalProof) {}); err != nil {
		t.Fatalf("a valid, unexpired, unused approval of the scope was refused: %v", err)
	}
}

func TestAnApprovalIsGoodUntilTheInstantItExpires(t *testing.T) {
	t.Parallel()
	err := checkApprovalWith(func(p *ApprovalProof) { p.Approval.ExpiresAt = testNow.Add(time.Nanosecond) })
	if err != nil {
		t.Fatalf("an approval that expires a moment from now was refused: %v", err)
	}
}

func TestOwnerApprovedCannotBeEmittedWithoutAnApproval(t *testing.T) {
	t.Parallel()
	requireGuardFailed(t, "no approval guard wired",
		machineWith(Guards{}).Check(context.Background(), ownerApprovedRequest()), GuardApproval)

	empty := Guards{Approval: approvalWith(func(core.Transition) ApprovalProof { return ApprovalProof{} })}
	requireGuardFailed(t, "empty proof",
		machineWith(empty).Check(context.Background(), ownerApprovedRequest()), GuardApproval)

	// Proof of delegation says no approval is needed; it is not an approval.
	delegated := Guards{Delegation: delegationWith(validDelegation)}
	requireGuardFailed(t, "delegation offered instead of an approval",
		machineWith(delegated).Check(context.Background(), ownerApprovedRequest()), GuardApproval)
}

func TestARequestThatWasNotApprovedDoesNotMakeAnIssueReady(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*ApprovalProof){
		"the request is still pending":    func(p *ApprovalProof) { p.Request.Status = approvals.StatusPending },
		"the request was rejected":        func(p *ApprovalProof) { p.Request.Status = approvals.StatusRejected },
		"changes were requested":          func(p *ApprovalProof) { p.Request.Status = approvals.StatusChangesRequested },
		"the request expired undecided":   func(p *ApprovalProof) { p.Request.Status = approvals.StatusExpired },
		"the request status is unknown":   func(p *ApprovalProof) { p.Request.Status = "open" },
		"the decision is a rejection":     func(p *ApprovalProof) { p.Approval.Decision = approvals.DecisionReject },
		"the decision asks for changes":   func(p *ApprovalProof) { p.Approval.Decision = approvals.DecisionRequestChanges },
		"the decision is unknown":         func(p *ApprovalProof) { p.Approval.Decision = "ok" },
		"no decision was recorded":        func(p *ApprovalProof) { p.Approval = approvals.Approval{} },
		"no request was presented":        func(p *ApprovalProof) { p.Request = approvals.Request{} },
		"the request has no risk summary": func(p *ApprovalProof) { p.Request.RiskSummary = "" },
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkApprovalWith(mutate), GuardApproval)
	}
}

func TestAnApprovalAlreadyUsedDoesNotMakeAnIssueReadyAgain(t *testing.T) {
	t.Parallel()
	err := checkApprovalWith(func(p *ApprovalProof) { p.Request.Status = approvals.StatusConsumed })
	requireGuardFailed(t, "consumed approval", err, GuardApproval)
	if got := detailsOf(err)["status"]; got != string(approvals.StatusConsumed) {
		t.Errorf("refusal reports request status %q, want %s", got, approvals.StatusConsumed)
	}
}

func TestAnExpiredApprovalDoesNotMakeAnIssueReady(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*ApprovalProof){
		"expires at this very instant": func(p *ApprovalProof) { p.Approval.ExpiresAt = testNow },
		"expired a moment ago":         func(p *ApprovalProof) { p.Approval.ExpiresAt = testNow.Add(-time.Nanosecond) },
		"states no expiry":             func(p *ApprovalProof) { p.Approval.ExpiresAt = time.Time{} },
		"its expiry condition has passed": func(p *ApprovalProof) {
			p.Approval.Conditions.ExpiresAt = testNow.Add(-time.Minute)
		},
		"its expiry condition falls now": func(p *ApprovalProof) { p.Approval.Conditions.ExpiresAt = testNow },
	}
	for name, mutate := range cases {
		err := checkApprovalWith(mutate)
		requireGuardFailed(t, name, err, GuardApproval)
		if got := detailsOf(err)["cause"]; got != string(core.CodeExpired) {
			t.Errorf("%s: refusal reports cause %q, want %s", name, got, core.CodeExpired)
		}
	}
}

func TestAnApprovalOfAnotherScopeOrPolicyDoesNotMakeAnIssueReady(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*ApprovalProof){
		"the scope was edited after it was approved": func(p *ApprovalProof) { p.ScopeDigest = testDigest },
		"the policy was revised after the approval":  func(p *ApprovalProof) { p.PolicyRevision = otherSHA },
		"the scope in force is unknown":              func(p *ApprovalProof) { p.ScopeDigest = "" },
		"the scope in force is not a digest":         func(p *ApprovalProof) { p.ScopeDigest = "sow-v2" },
		"the policy in force is unknown":             func(p *ApprovalProof) { p.PolicyRevision = "" },
		"the policy in force is a branch":            func(p *ApprovalProof) { p.PolicyRevision = "main" },
		"the approval is for another issue":          func(p *ApprovalProof) { p.Request.IssueID = otherIssue },
		"the approval concerns no single issue":      func(p *ApprovalProof) { p.Request.IssueID = core.IssueRef{} },
		"the decision answers another request":       func(p *ApprovalProof) { p.Approval.RequestID = "apr-0002" },
		"the decision carries another nonce":         func(p *ApprovalProof) { p.Approval.Nonce = "0000000000000000" },
		"the decision covers another subject than the request": func(p *ApprovalProof) {
			p.Request.SubjectDigest = testDigest
		},
		"the decision was made under another policy than the request": func(p *ApprovalProof) {
			p.Request.PolicyRevision = otherSHA
		},
		"request and decision agree on a scope that is no longer in force": func(p *ApprovalProof) {
			p.Request.SubjectDigest, p.Approval.SubjectDigest = testDigest, testDigest
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkApprovalWith(mutate), GuardApproval)
	}
}

func TestAnApprovalMustBeSignedAndDecidedByAPerson(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*ApprovalProof){
		"the decision is unsigned":        func(p *ApprovalProof) { p.Approval.Signature = core.Signature{} },
		"the signature names no key":      func(p *ApprovalProof) { p.Approval.Signature.KeyID = "" },
		"an agent decided":                func(p *ApprovalProof) { p.Approval.ActorKind = core.ActorAgent },
		"a service decided":               func(p *ApprovalProof) { p.Approval.ActorKind = core.ActorService },
		"the approver is anonymous":       func(p *ApprovalProof) { p.Approval.ActorID = "" },
		"the decision is undated":         func(p *ApprovalProof) { p.Approval.DecidedAt = time.Time{} },
		"the decision is dated after now": func(p *ApprovalProof) { p.Approval.DecidedAt = testNow.Add(time.Nanosecond) },
		"the surface is unknown":          func(p *ApprovalProof) { p.Approval.Surface = "email" },
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkApprovalWith(mutate), GuardApproval)
	}
}

func TestADeniedApprovalBlocksTheIssueWithoutAnyProof(t *testing.T) {
	t.Parallel()
	// A rejected or expired request must always be able to leave
	// ApprovalPending for Blocked; only the way to Ready needs the approval.
	request := transitionOn(core.StateApprovalPending, core.EventApprovalDenied, core.StateBlocked)
	if err := machineWith(Guards{}).Check(context.Background(), request); err != nil {
		t.Fatalf("blocking an issue whose approval was denied was refused: %v", err)
	}
}

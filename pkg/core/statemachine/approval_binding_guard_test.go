package statemachine

import (
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// An approval makes an issue Ready only when it was asked for, and given, as
// an approval of that issue's scope in this factory, while the request could
// still be decided.

func TestAnApprovalOfAnotherActionDoesNotMakeAnIssueReady(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*ApprovalProof){
		"the request asks to publish a release": func(p *ApprovalProof) {
			p.Request.Action, p.Approval.Action = "release.publish", "release.publish"
		},
		"the request asks to change policy": func(p *ApprovalProof) {
			p.Request.Action, p.Approval.Action = "policy.change", "policy.change"
		},
		"the request was relabelled as a scope approval after the decision": func(p *ApprovalProof) {
			p.Approval.Action = "release.publish"
		},
		"the decision was relabelled as a scope approval": func(p *ApprovalProof) {
			p.Request.Action = "release.publish"
		},
		"the decision names no action": func(p *ApprovalProof) { p.Approval.Action = "" },
		"the request names no action":  func(p *ApprovalProof) { p.Request.Action = "" },
		"the action differs in letter case": func(p *ApprovalProof) {
			p.Request.Action, p.Approval.Action = "Scope.Approve", "Scope.Approve"
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkApprovalWith(mutate), GuardApproval)
	}
}

func TestAnApprovalOfAnotherFactoryDoesNotMakeAnIssueReady(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*ApprovalProof){
		"request and decision were made in another factory": func(p *ApprovalProof) {
			p.Request.FactoryID, p.Approval.FactoryID = otherFactory, otherFactory
		},
		"the request was moved to this factory after the decision": func(p *ApprovalProof) {
			p.Approval.FactoryID = otherFactory
		},
		"the decision claims this factory for another factory's request": func(p *ApprovalProof) {
			p.Request.FactoryID = otherFactory
		},
		"this machine serves another factory":        func(p *ApprovalProof) { p.FactoryID = otherFactory },
		"the factory this machine serves is unknown": func(p *ApprovalProof) { p.FactoryID = "" },
		"the decision names no factory":              func(p *ApprovalProof) { p.Approval.FactoryID = "" },
		"no factory is named anywhere": func(p *ApprovalProof) {
			p.FactoryID, p.Request.FactoryID, p.Approval.FactoryID = "", "", ""
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkApprovalWith(mutate), GuardApproval)
	}
}

func TestADecisionSignedForAnotherIssueDoesNotMakeAnIssueReady(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*ApprovalProof){
		"the request was moved to this issue after the decision": func(p *ApprovalProof) {
			p.Approval.IssueID = otherIssue
		},
		"the decision concerns no single issue": func(p *ApprovalProof) { p.Approval.IssueID = core.IssueRef{} },
		"request and decision are both about another issue": func(p *ApprovalProof) {
			p.Request.IssueID, p.Approval.IssueID = otherIssue, otherIssue
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkApprovalWith(mutate), GuardApproval)
	}
}

func TestAnApprovalDecidedOutsideTheLifeOfItsRequestDoesNotMakeAnIssueReady(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*ApprovalProof){
		"the request expired an hour before the decision": func(p *ApprovalProof) {
			p.Request.ExpiresAt = p.Approval.DecidedAt.Add(-time.Hour)
		},
		"the request expired a moment before the decision": func(p *ApprovalProof) {
			p.Request.ExpiresAt = p.Approval.DecidedAt.Add(-time.Nanosecond)
		},
		"the request expired at the instant of the decision": func(p *ApprovalProof) {
			p.Request.ExpiresAt = p.Approval.DecidedAt
		},
		"the decision is dated before the request was made": func(p *ApprovalProof) {
			p.Approval.DecidedAt = p.Request.CreatedAt.Add(-time.Nanosecond)
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkApprovalWith(mutate), GuardApproval)
	}
	// A request may lapse after it was decided in time: the approval then
	// lives until its own expiry.
	lapsedSince := checkApprovalWith(func(p *ApprovalProof) {
		p.Request.ExpiresAt = p.Approval.DecidedAt.Add(time.Nanosecond)
	})
	if lapsedSince != nil {
		t.Fatalf("an approval decided a moment before its request lapsed was refused: %v", lapsedSince)
	}
}

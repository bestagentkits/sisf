package statemachine

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// An issue leaves scoping for Ready in one of two ways: policy lets its scope
// proceed without the Owner, or the Owner approved it. The guards below
// supply the facts of each; neither stands in for the other.

// DelegationProof is the policy's answer on one scope of work.
type DelegationProof struct {
	// IssueID is the issue whose scope was judged. It must be the issue of
	// the transition.
	IssueID core.IssueRef
	// ScopeDigest identifies the exact scope of work that was judged.
	ScopeDigest core.Digest
	// PolicyRevision is the Owner-approved policy commit it was judged under.
	PolicyRevision core.CommitSHA
	// Decision says whether the scope may proceed without the Owner. Only an
	// outright allow is within the delegation; a new feature, a wider
	// permission or anything else that needs the Owner is not.
	Decision core.AuthzDecision
}

// DelegationGuard proves that a scope lies within what the Owner delegated,
// so it needs no approval. When it does not, the issue can only take the
// approval request to ApprovalPending.
type DelegationGuard interface {
	DelegationProof(ctx context.Context, t core.Transition) (DelegationProof, error)
}

// ApprovalProof is the Owner's decision on a scope, next to what is in force
// now.
type ApprovalProof struct {
	// Request is the approval request and Approval the decision recorded for
	// it, both as the approval store holds them.
	Request  approvals.Request
	Approval approvals.Approval
	// ScopeDigest identifies the scope of work attached to the issue now. An
	// edit after the decision changes it and voids the approval.
	ScopeDigest core.Digest
	// PolicyRevision is the policy commit in force now.
	PolicyRevision core.CommitSHA
}

// ApprovalGuard proves that the Owner approved the scope of an issue. The
// implementation must return only a decision whose signature it has
// verified, over the approval's SigningPayload, against the trusted approval
// keys, and whose actor was authorized to decide; the machine cannot check
// either. The machine checks everything else: that request and decision
// belong together and to the issue, that a person approved, that the
// approval is unexpired and unused, and that it covers the scope and the
// policy in force.
//
// The guard only reads. The step is the approval's one use: the caller
// spends it with approvals.Store.Consume in the same unit of work as the
// write, after the machine has let the step through.
type ApprovalGuard interface {
	ApprovalProof(ctx context.Context, t core.Transition) (ApprovalProof, error)
}

// DelegationGuardFunc adapts a function to DelegationGuard.
type DelegationGuardFunc func(ctx context.Context, t core.Transition) (DelegationProof, error)

// DelegationProof calls f.
func (f DelegationGuardFunc) DelegationProof(ctx context.Context, t core.Transition) (DelegationProof, error) {
	return f(ctx, t)
}

// ApprovalGuardFunc adapts a function to ApprovalGuard.
type ApprovalGuardFunc func(ctx context.Context, t core.Transition) (ApprovalProof, error)

// ApprovalProof calls f.
func (f ApprovalGuardFunc) ApprovalProof(ctx context.Context, t core.Transition) (ApprovalProof, error) {
	return f(ctx, t)
}

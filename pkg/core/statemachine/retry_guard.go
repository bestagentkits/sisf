package statemachine

import (
	"context"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// BlockCause says why an issue is in Blocked. The lifecycle has two ways in:
// a scope the Owner did not approve, and a release that was rolled back.
type BlockCause string

const (
	// BlockApprovalDenied means the Owner rejected the scope or asked for
	// changes to it.
	BlockApprovalDenied BlockCause = "approval_denied"
	// BlockApprovalExpired means the approval request lapsed undecided, or
	// the approval lapsed unused.
	BlockApprovalExpired BlockCause = "approval_expired"
	// BlockRolledBack means a release of the issue was rolled back and its
	// recovery was reported.
	BlockRolledBack BlockCause = "rolled_back"
)

// Valid reports whether c is a known cause.
func (c BlockCause) Valid() bool {
	return c == BlockApprovalDenied || c == BlockApprovalExpired || c == BlockRolledBack
}

// NeedsScopeProof reports whether the block came from an approval that was
// not given. Such an issue never had a scope it may be worked on, so a retry
// has to bring one.
func (c BlockCause) NeedsScopeProof() bool {
	return c == BlockApprovalDenied || c == BlockApprovalExpired
}

// RetryProof is what is known about one request to retry a blocked issue.
type RetryProof struct {
	// IssueID is the issue the retry was judged for. It must be the issue of
	// the transition.
	IssueID core.IssueRef
	// Cause is why the issue is blocked and BlockedAt when it was. Both are
	// read from the record of the step that blocked it, never from the
	// request to retry.
	Cause     BlockCause
	BlockedAt time.Time
	// Actor is the authenticated party asking for the retry, as the
	// authentication layer built it. It must be the actor the transition
	// names. An agent cannot retry.
	Actor core.Actor
	// Decision is the authorization decision on that actor retrying this
	// issue. Only an outright allow counts.
	Decision core.AuthzDecision
	// Approval and Delegation prove the scope of an issue that was blocked
	// because its approval was denied or expired: an approval the Owner gave
	// after the block, or the policy's answer that the scope now lies within
	// the delegation. One of them is required for such an issue, each one
	// presented must hold, and neither is needed after a rollback.
	Approval   *ApprovalProof
	Delegation *DelegationProof
}

// RetryGuard supplies the facts about a request to send a blocked issue back
// to Ready. An authorization to retry is not enough to undo the Owner's
// refusal: the machine lets an issue blocked over its approval retry only on
// the same proof that would have made it Ready in the first place.
//
// The approval and the delegation are held to the rules of ApprovalGuard and
// DelegationGuard: a decision is returned only when its signature was
// verified and its actor was authorized, and an approval the retry uses is
// spent with approvals.Store.Consume in the same unit of work as the write.
type RetryGuard interface {
	RetryProof(ctx context.Context, t core.Transition) (RetryProof, error)
}

// RetryGuardFunc adapts a function to RetryGuard.
type RetryGuardFunc func(ctx context.Context, t core.Transition) (RetryProof, error)

// RetryProof calls f.
func (f RetryGuardFunc) RetryProof(ctx context.Context, t core.Transition) (RetryProof, error) {
	return f(ctx, t)
}

// checkRetry requires the retry to name its actor, the proof to be about
// that actor and this issue, the actor to be a person or a service that is
// allowed outright, and the cause of the block to be known. A decision that
// still needs an approval or a step-up is not an authorization. An issue
// blocked over its approval also needs the scope proof.
func checkRetry(t core.Transition, p RetryProof, now time.Time) error {
	switch {
	case strings.TrimSpace(t.ActorID) == "":
		return refuse(GuardRetry, t, "a retry must name the actor who asked for it")
	case p.IssueID != t.IssueID:
		return refuse(GuardRetry, t, "the retry was judged for another issue")
	case p.Actor.ID != t.ActorID:
		return refuse(GuardRetry, t, "the retry was authorized for another actor than the one asking")
	case !p.Actor.Kind.Valid():
		return refuse(GuardRetry, t, "it is not known whether a person, a service or an agent asked for the retry")
	case p.Actor.Kind == core.ActorAgent:
		return refuse(GuardRetry, t, "an agent cannot retry a blocked issue")
	case !p.Cause.Valid():
		return refuse(GuardRetry, t, "it is not known why the issue is blocked")
	}
	if !p.Decision.Allowed() {
		refusal := refuse(GuardRetry, t, "the retry is not authorized")
		refusal.Details["effect"] = string(p.Decision.Effect)
		if p.Decision.Reason != "" {
			refusal.Details["reason"] = p.Decision.Reason
		}
		return refusal
	}
	if !p.Cause.NeedsScopeProof() {
		return nil
	}
	return checkRetriedScope(t, p, now)
}

// checkRetriedScope requires, for an issue blocked over its approval, an
// approval decided after the block or proof of delegation. Whatever is
// presented must hold by the rule of its own guard.
func checkRetriedScope(t core.Transition, p RetryProof, now time.Time) error {
	switch {
	case p.BlockedAt.IsZero() || p.BlockedAt.After(now):
		return refuse(GuardRetry, t, "it is not known when the issue was blocked")
	case p.Approval == nil && p.Delegation == nil:
		refusal := refuse(GuardRetry, t, "the scope was not approved: a retry needs a new approval or proof of delegation")
		refusal.Details["cause"] = string(p.Cause)
		return refusal
	}
	if p.Approval != nil {
		if err := checkApproval(t, *p.Approval, now); err != nil {
			return refuseAs(GuardRetry, t, err)
		}
		if !p.Approval.Approval.DecidedAt.After(p.BlockedAt) {
			return refuse(GuardRetry, t, "the approval was decided before the issue was blocked and cannot lift the block")
		}
	}
	if p.Delegation != nil {
		if err := checkDelegation(t, *p.Delegation); err != nil {
			return refuseAs(GuardRetry, t, err)
		}
	}
	return nil
}

package intake

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// DenialReason says why a scope did not get the approval it needed.
type DenialReason string

const (
	// DenialRejected: the Owner rejected the scope.
	DenialRejected DenialReason = "approval_rejected"
	// DenialExpired: the request or the approval lapsed before it was used.
	DenialExpired DenialReason = "approval_expired"
	// DenialUnusable: the approval the issue waited on was used elsewhere.
	DenialUnusable DenialReason = "approval_unusable"
)

// Denial records that intake blocked an issue because its scope needed the
// Owner and did not get an approval. It is written in the same transaction
// as the step to Blocked, and only the retry that lifts the block removes
// it.
type Denial struct {
	Reason DenialReason `json:"reason"`
	// RequestID is the approval request that was not approved.
	RequestID string `json:"request_id"`
	// ScopeDigest and PolicyRevision are what the request was about.
	ScopeDigest    core.Digest    `json:"scope_digest"`
	PolicyRevision core.CommitSHA `json:"policy_revision"`
	At             time.Time      `json:"at"`
}

// Cause is why the lifecycle holds the issue in Blocked. A request or an
// approval that lapsed is an expired approval. A rejection is a denied one,
// and so is an approval that went to something else: either way the scope
// has no approval left.
func (d Denial) Cause() statemachine.BlockCause {
	if d.Reason == DenialExpired {
		return statemachine.BlockApprovalExpired
	}
	return statemachine.BlockApprovalDenied
}

// ReadDenial returns the denial recorded for an issue, or NOT_FOUND when
// intake did not block it for want of an approval. It reads through kv, so
// a caller that is inside a store transaction passes that transaction.
func ReadDenial(ctx context.Context, kv state.KV, issue core.IssueRef) (Denial, error) {
	entry, err := kv.GetKV(ctx, namespace, issueKey(issue))
	if err != nil {
		if core.HasCode(err, core.CodeNotFound) {
			return Denial{}, core.NewError(core.CodeNotFound, "intake has no record of this issue")
		}
		return Denial{}, err
	}
	var doc issueDoc
	if err := decode(entry, &doc); err != nil {
		return Denial{}, err
	}
	if doc.Denial == nil {
		return Denial{}, core.NewError(core.CodeNotFound, "the issue was not blocked for want of an approval")
	}
	return *doc.Denial, nil
}

// ApprovalDenial is ReadDenial in a transaction of its own.
func (s *Service) ApprovalDenial(ctx context.Context, issue core.IssueRef) (Denial, error) {
	var denial Denial
	err := s.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		denial, err = ReadDenial(ctx, tx, issue)
		return err
	})
	return denial, err
}

// RetryGuard returns the retry guard of the lifecycle with one rule put in
// front of next: an issue that is blocked because its scope was not approved
// does not go back to Ready through this guard, whoever asks and whatever
// next would answer. Such an issue has no permitted work to repeat. It
// returns to Ready only through Retry, which presents an approval given
// after the block or proof of delegation and spends the approval as it
// writes the step; a guard only reads and could not.
//
// Every other retry is left to next. A nil next proves nothing, so the
// lifecycle refuses the step, and so does a denial record that cannot be
// read. The guard reads in a transaction of its own, so it is asked before
// the caller opens one.
func (s *Service) RetryGuard(next statemachine.RetryGuard) statemachine.RetryGuard {
	return statemachine.RetryGuardFunc(func(ctx context.Context, t core.Transition) (statemachine.RetryProof, error) {
		denial, err := s.ApprovalDenial(ctx, t.IssueID)
		switch {
		case err == nil:
			refusal := core.NewError(core.CodeGuardFailed, "the scope of this issue was not approved ("+string(denial.Reason)+
				"); it returns to Ready only on a new approval or proof of delegation, through the retry of intake")
			refusal.Details = map[string]string{"guard": string(statemachine.GuardRetry), "cause": string(denial.Cause())}
			return statemachine.RetryProof{}, refusal
		case !core.HasCode(err, core.CodeNotFound):
			return statemachine.RetryProof{}, err
		case next == nil:
			return statemachine.RetryProof{}, nil
		}
		return next.RetryProof(ctx, t)
	})
}

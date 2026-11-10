package approvals

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// SpendFunc is the caller's part of the unit of work an approval is spent
// in. It gets the request and the decision as the store holds them, the
// decision's signature already verified, and writes through tx. Returning an
// error undoes everything, the spending included.
type SpendFunc func(ctx context.Context, tx state.Tx, request Request, approval Approval) error

// Consume spends the approval on subject under policy, atomically and once.
//
// An approval that was already used is REPLAYED. A request still waiting is
// APPROVAL_REQUIRED, and one that lapsed undecided is EXPIRED. The recorded
// decision must still carry the request's binding and a signature over
// Approval.SigningPayload that verifies, otherwise SIGNATURE_INVALID: a
// record edited in the database, or signed by a key since revoked,
// authorizes nothing. Then Approval.Authorizes applies at the current time:
// a rejection is PERMISSION_DENIED, another subject or policy revision is
// APPROVAL_REQUIRED, a lapsed approval EXPIRED.
//
// A refusal leaves the approval as it was, so a caller that asked with an
// edited subject has not burnt it. The use is announced in the audit log
// before anything is spent, and nothing is spent when that fails. A use that
// took effect but whose final audit record could not be written is returned
// as an error with the approval already spent: the caller must not act, and
// the approval cannot be used again.
func (s *KVStore) Consume(ctx context.Context, requestID string, subject core.Digest, policy core.CommitSHA) (Approval, error) {
	return s.spend(ctx, requestID, subject, policy, nil)
}

// ConsumeWith spends the approval as Consume does and runs write in the same
// transaction, so the step an approval permits and the spending of it happen
// together or not at all. This is how a lifecycle step that needs an approval
// is written: write judges the request and the decision it is handed, then
// writes the step.
//
// write runs before the approval is marked used, so the request it sees is
// still approved. An error from write is returned as it is and leaves the
// approval unspent. The transaction is not repeated when it loses a race:
// what write does rests on what its caller read, so the caller decides.
func (s *KVStore) ConsumeWith(ctx context.Context, requestID string, subject core.Digest, policy core.CommitSHA, write SpendFunc) (Approval, error) {
	if write == nil {
		return Approval{}, invalid("spending an approval with a write needs the write")
	}
	return s.spend(ctx, requestID, subject, policy, write)
}

func (s *KVStore) spend(ctx context.Context, requestID string, subject core.Digest, policy core.CommitSHA, write SpendFunc) (Approval, error) {
	// Look first without writing: a use that would be refused is refused
	// here, and one that would pass is announced before anything is spent.
	var request Request
	err := s.state.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		request, _, _, err = s.spendable(ctx, tx, requestID, subject, policy)
		return err
	})
	actor := s.service(request.RequestedBy)
	if err != nil {
		return Approval{}, s.record(ctx, request, actionConsume, actor, err)
	}
	if err := s.append(ctx, request, actionConsume, actor, core.OutcomeAllowed, nil); err != nil {
		return Approval{}, core.NewError(core.CodeOf(err), "the use could not be audited, so the approval was not spent").WithCause(err)
	}

	var spent Approval
	refused := false
	run := s.update
	if write != nil {
		run = s.state.Update
	}
	err = run(ctx, func(ctx context.Context, tx state.Tx) error {
		r, version, a, err := s.spendable(ctx, tx, requestID, subject, policy)
		if refused = err != nil; refused {
			return err
		}
		if write != nil {
			if err := write(ctx, tx, r, a); err != nil {
				return err
			}
		}
		r.Status = StatusConsumed
		if err := put(ctx, tx, requestKey(r.ID), version, r); err != nil {
			return err
		}
		spent = a
		return nil
	})
	switch {
	case err != nil && refused:
		// Another caller spent it between the look and the write.
		return Approval{}, s.record(ctx, request, actionConsume, actor, err)
	case err != nil:
		// The announced use did not happen. The error is what the caller
		// needs; a failed audit of it adds nothing.
		_ = s.append(ctx, request, actionConsume, actor, core.OutcomeFailed, err)
		return Approval{}, err
	}
	if err := s.record(ctx, request, actionConsume, actor, nil); err != nil {
		return Approval{}, err
	}
	return spent, nil
}

// spendable reads a request and its decision and reports whether the
// approval can be spent on subject under policy right now. It writes
// nothing. The request is returned even with an error, for the audit record.
func (s *KVStore) spendable(ctx context.Context, tx state.Tx, requestID string, subject core.Digest, policy core.CommitSHA) (Request, int64, Approval, error) {
	r, version, err := loadRequest(ctx, tx, requestID)
	if err != nil {
		return Request{}, 0, Approval{}, err
	}
	switch s.current(r).Status {
	case StatusConsumed:
		return r, 0, Approval{}, core.NewError(core.CodeReplayed, "the approval was already used")
	case StatusPending:
		return r, 0, Approval{}, core.NewError(core.CodeApprovalRequired, "the approval request has not been decided")
	case StatusExpired:
		return r, 0, Approval{}, core.NewError(core.CodeExpired, "the approval request lapsed without a decision")
	}
	a, err := loadDecision(ctx, tx, requestID)
	if err != nil {
		return r, 0, Approval{}, err
	}
	if err := s.authentic(ctx, r, a); err != nil {
		return r, 0, Approval{}, err
	}
	if err := a.Authorizes(subject, policy, s.clock.Now()); err != nil {
		return r, 0, Approval{}, err
	}
	return r, version, a, nil
}

// authentic reports whether the stored decision is the one the store signed
// for this request. The fields of a record are trusted only after this.
func (s *KVStore) authentic(ctx context.Context, r Request, a Approval) error {
	if a.RequestID != r.ID || a.SubjectDigest != r.SubjectDigest || a.PolicyRevision != r.PolicyRevision || a.Nonce != r.Nonce {
		return core.NewError(core.CodeSignatureInvalid, "the recorded decision does not belong to this approval request")
	}
	if err := s.verifier.Verify(ctx, a.SigningPayload(), a.Signature); err != nil {
		if core.HasCode(err, core.CodeSignatureInvalid) {
			return err
		}
		// The verifier could not answer. That is not proof, so nothing is spent.
		return core.NewError(core.CodeSignatureInvalid, "the signature of the decision could not be verified").WithCause(err)
	}
	return nil
}

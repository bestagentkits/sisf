package qa

import (
	"context"
	"crypto/ed25519"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/core"
)

// ReceiptBundle is what an outside party needs, beside the Owner's root
// public key, to check a QA receipt: the receipt and the root-signed
// delegation of the short-lived key that signed it. Both are public.
type ReceiptBundle struct {
	Receipt core.QAReceipt `json:"receipt"`
	// Delegation is the delegation of the key named by the receipt's
	// signature. Without it the receipt cannot be traced to the root key.
	Delegation *delegation.SignedDelegation `json:"delegation,omitempty"`
}

// ReceiptClaim is what the party checking a receipt expects it to be
// about. The factory, issue and run are required; a commit is checked only
// when named.
type ReceiptClaim struct {
	FactoryID    core.FactoryID
	IssueID      core.IssueRef
	RunID        core.RunID
	CandidateSHA core.CommitSHA
}

// ReceiptCheck is a receipt that verified, with the key that signed it and
// the delegation that made that key trusted. Receipt holds only signed
// fields.
type ReceiptCheck struct {
	RootKeyID    string             `json:"root_key_id"`
	KeyID        string             `json:"key_id"`
	Purpose      delegation.Purpose `json:"purpose"`
	Holder       delegation.Holder  `json:"holder"`
	KeyNotBefore time.Time          `json:"key_not_before"`
	KeyNotAfter  time.Time          `json:"key_not_after"`
	// JudgedAt is the instant the key's window was judged at: the
	// receipt's issue time.
	JudgedAt time.Time `json:"judged_at"`
	// RevocationSequence is the sequence of the root-signed revocation list
	// checked, 0 when none was given.
	RevocationSequence int64 `json:"revocation_sequence"`
	HardGatesPassed    bool  `json:"hard_gates_passed"`
	// Expired says the receipt may no longer authorize a release now; its
	// signature still proves what the evaluator concluded.
	Expired bool           `json:"expired"`
	Receipt core.QAReceipt `json:"receipt"`
}

// VerifyReceiptBundle checks a receipt with nothing but the Owner's root
// public key. The delegation must be signed by root for want's factory and
// name the receipt's signing key for QA receipts; the receipt must be
// complete, signed by that key while its delegation was valid and not
// revoked by revocations (a root-signed list, nil when none is at hand),
// and bound to want's factory, issue, run and, when named, commit.
//
// The key's window is judged as of the receipt's issue time, so a receipt
// stays verifiable after its week-long key has expired. A key that leaked
// could then sign a receipt dated inside its old window; only a revocation
// list stops that, so a check without one reports RevocationSequence 0.
//
// The errors are INVALID_ARGUMENT for an incomplete receipt, claim or
// bundle, SIGNATURE_INVALID for a receipt the root key does not vouch for,
// and GUARD_FAILED for a receipt about another issue, run or commit.
func VerifyReceiptBundle(ctx context.Context, root ed25519.PublicKey, b ReceiptBundle, want ReceiptClaim,
	revocations *delegation.SignedRevocationList, now time.Time) (ReceiptCheck, error) {
	if err := want.FactoryID.Validate(); err != nil {
		return ReceiptCheck{}, err
	}
	if err := want.IssueID.Validate(); err != nil {
		return ReceiptCheck{}, err
	}
	if err := want.RunID.Validate(); err != nil {
		return ReceiptCheck{}, err
	}
	if b.Delegation == nil {
		return ReceiptCheck{}, invalid("the bundle carries no delegation of the key that signed the receipt")
	}
	r := b.Receipt
	if err := r.Validate(); err != nil {
		return ReceiptCheck{}, err
	}
	verifier, err := delegation.NewChainVerifier(delegation.VerifierConfig{FactoryID: want.FactoryID, Root: root, Clock: instant(r.IssuedAt)})
	if err != nil {
		return ReceiptCheck{}, err
	}
	if err := verifier.Trust(ctx, *b.Delegation); err != nil {
		return ReceiptCheck{}, notVouched(err)
	}
	check := ReceiptCheck{RootKeyID: signing.KeyID(root), JudgedAt: r.IssuedAt}
	if revocations != nil {
		if err := verifier.ApplyRevocations(ctx, *revocations); err != nil {
			return ReceiptCheck{}, err
		}
		check.RevocationSequence = revocations.List.Sequence
	}
	d := b.Delegation.Delegation
	if r.Signature.KeyID != d.KeyID {
		return ReceiptCheck{}, core.NewError(core.CodeSignatureInvalid, "the delegation is for key "+d.KeyID+", not the key "+r.Signature.KeyID+" that signed the receipt")
	}
	if err := verifier.Verify(ctx, r.SigningPayload(), r.Signature); err != nil {
		return ReceiptCheck{}, notVouched(err)
	}
	switch {
	case r.FactoryID != want.FactoryID:
		return ReceiptCheck{}, mismatch("another factory")
	case r.IssueID != want.IssueID || r.RunID != want.RunID:
		return ReceiptCheck{}, mismatch("another issue or run")
	case want.CandidateSHA != "" && r.CandidateSHA != want.CandidateSHA:
		return ReceiptCheck{}, mismatch("another commit")
	}
	check.KeyID, check.Purpose, check.Holder = d.KeyID, d.Purpose, d.Holder
	check.KeyNotBefore, check.KeyNotAfter = d.NotBefore, d.NotAfter
	check.HardGatesPassed, check.Expired, check.Receipt = r.HardGatesPassed(), r.Expired(now), r
	return check, nil
}

// notVouched reports a delegation or signature the root key does not vouch
// for as SIGNATURE_INVALID, keeping the cause.
func notVouched(err error) error {
	if core.HasCode(err, core.CodeSignatureInvalid) {
		return err
	}
	return core.NewError(core.CodeSignatureInvalid, "the root key does not vouch for the key that signed the receipt").WithCause(err)
}

// instant is a clock stopped at one instant.
type instant time.Time

func (c instant) Now() time.Time { return time.Time(c) }
func (c instant) After(d time.Duration) <-chan time.Time {
	fired := make(chan time.Time, 1)
	fired <- time.Time(c).Add(d)
	return fired
}

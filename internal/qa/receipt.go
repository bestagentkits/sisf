package qa

import (
	"context"
	"strconv"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// issue builds, signs and checks the receipt of one evaluation.
func (e *Evaluator) issue(ctx context.Context, c qa.Candidate, route routeChoice, checks []core.QACheck) (core.QAReceipt, error) {
	now := e.cfg.Clock.Now().UTC()
	receipt := core.QAReceipt{
		FactoryID: e.cfg.FactoryID, IssueID: c.IssueID, RunID: c.RunID,
		CandidateSHA: c.CommitSHA, CandidateDigest: c.Digest, SuiteDigest: c.SuiteDigest,
		BaselineSHA: c.BaselineSHA, PolicySHA: c.Policy.PolicySHA,
		QAAgentIdentity: e.cfg.Identity, CodingAgentIdentity: c.CodingAgentIdentity,
		CodingModelRoute: c.CodingModelRoute, QAModelRoute: route.route, SameModelRoute: route.same,
		Checks: checks, IssuedAt: now, ExpiresAt: now.Add(e.cfg.ReceiptTTL), EvaluatorAttempt: c.Attempt,
	}
	for _, check := range checks {
		if check.Kind == core.CheckRubric {
			receipt.Score += check.Points
		}
	}
	sig, err := e.cfg.Signer.Sign(ctx, receipt.SigningPayload())
	if err != nil {
		return core.QAReceipt{}, err
	}
	receipt.Signature = sig
	if err := VerifyReceipt(ctx, e.cfg.Verifier, receipt, e.cfg.FactoryID, c, now); err != nil {
		return core.QAReceipt{}, err
	}
	return receipt, nil
}

// VerifyReceipt accepts a receipt only when it is complete, signed by a key
// the verifier trusts now, issued no later than now and unexpired, and bound
// to this factory and to the candidate: its issue, run, commit, digest,
// suite, baseline, policy, coder and attempt. A receipt of another commit or
// build is never borrowed.
//
// The errors are INVALID_ARGUMENT for an incomplete receipt,
// SIGNATURE_INVALID for a bad, revoked or expired key, EXPIRED for a stale
// receipt and GUARD_FAILED for a receipt bound to anything else.
func VerifyReceipt(ctx context.Context, verifier core.Verifier, r core.QAReceipt, factory core.FactoryID, c qa.Candidate, now time.Time) error {
	if verifier == nil {
		return invalid("verifying a receipt needs a verifier")
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if err := verifier.Verify(ctx, r.SigningPayload(), r.Signature); err != nil {
		return core.NewError(core.CodeSignatureInvalid, "the QA receipt is not signed by a trusted evaluator key").WithCause(err)
	}
	switch {
	case r.IssuedAt.After(now):
		return core.NewError(core.CodeGuardFailed, "the QA receipt is issued in the future")
	case r.Expired(now):
		return core.NewError(core.CodeExpired, "the QA receipt has expired")
	case r.FactoryID != factory:
		return mismatch("another factory")
	case r.IssueID != c.IssueID || r.RunID != c.RunID:
		return mismatch("another issue or run")
	case r.CandidateSHA != c.CommitSHA || r.CandidateDigest != c.Digest:
		return mismatch("another commit or build")
	case r.SuiteDigest != c.SuiteDigest || r.BaselineSHA != c.BaselineSHA || r.PolicySHA != c.Policy.PolicySHA:
		return mismatch("another suite, baseline or policy")
	case !core.SameIdentity(r.CodingAgentIdentity, c.CodingAgentIdentity) || r.CodingModelRoute != c.CodingModelRoute:
		return mismatch("another coding agent or route")
	case r.EvaluatorAttempt != c.Attempt:
		return mismatch("another evaluator attempt")
	}
	return nil
}

// mismatch builds the refusal of a receipt bound to something else.
func mismatch(what string) *core.Error {
	return core.NewError(core.CodeGuardFailed, "the QA receipt was issued for "+what)
}

// QAFailedKey is the idempotency key of the qa_failed step of one evaluator
// attempt. The agent zone may write that step only under the "qa/" prefix.
func QAFailedKey(run core.RunID, attempt int) string {
	return "qa/" + string(run) + "/" + strconv.Itoa(attempt)
}

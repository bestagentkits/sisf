package runactions

import (
	"context"

	"github.com/bestagentkits/sisf/internal/actions"
	iqa "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/pkg/core"
)

// QAReceipts reads the signed QA receipt of one evaluator attempt of a run
// with the delegation of its signing key; *handoff.ReadQAReceipt over the
// store is one. NOT_FOUND means the attempt has no receipt.
type QAReceipts interface {
	QAReceipt(ctx context.Context, run core.RunID, attempt int) (iqa.ReceiptBundle, string, error)
}

// receiptInput names the issue or run and, optionally, the evaluator
// attempt; the issue's current attempt when zero.
type receiptInput struct {
	Issue   string     `json:"issue,omitempty"`
	RunID   core.RunID `json:"run_id,omitempty"`
	Attempt int        `json:"attempt,omitempty"`
}

// ReceiptExport is what runs.receipt returns: a bundle the Owner or an
// auditor checks offline with the root public key alone (`sisf receipt
// verify`). The factory vouches for nothing here; it only hands the signed
// records out.
type ReceiptExport struct {
	iqa.ReceiptBundle
	FactoryID core.FactoryID `json:"factory_id"`
	Issue     string         `json:"issue"`
	RunID     core.RunID     `json:"run_id"`
	Attempt   int            `json:"attempt"`
	// Ref is where the supervisor keeps the receipt.
	Ref string `json:"ref"`
	// VerifyWith is the command that checks the bundle saved to a file.
	VerifyWith string `json:"verify_with"`
}

func (h handlers) runsReceipt(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	if h.QAReceipts == nil {
		return core.ActionResult{}, core.NewError(core.CodeNotSupported, "this factory does not serve QA receipts")
	}
	in, err := actions.DecodeInput[receiptInput](req)
	if err != nil {
		return core.ActionResult{}, err
	}
	if in.Attempt < 0 {
		return core.ActionResult{}, core.NewError(core.CodeInvalidArgument, "attempt must be at least 1")
	}
	target, err := h.resolveTarget(ctx, targetInput{Issue: in.Issue, RunID: in.RunID})
	if err != nil {
		return core.ActionResult{}, err
	}
	record := target.Record
	if record.RunID == "" {
		return core.ActionResult{}, core.NewError(core.CodeNotFound, "the issue has no run, so QA signed no receipt for it")
	}
	attempt := in.Attempt
	if attempt == 0 {
		attempt = record.Attempt
	}
	bundle, ref, err := h.QAReceipts.QAReceipt(ctx, record.RunID, attempt)
	if err != nil {
		return core.ActionResult{}, err
	}
	out := ReceiptExport{ReceiptBundle: bundle, FactoryID: h.FactoryID, Issue: record.IssueID.String(), RunID: record.RunID,
		Attempt: attempt, Ref: ref,
		VerifyWith: "sisf receipt verify --root-key <root public key file> --factory " + string(h.FactoryID) +
			" --issue " + record.IssueID.String() + " --run " + string(record.RunID) + " <this output saved to a file>"}
	if bundle.Delegation == nil {
		out.VerifyWith += " --delegation <delegation-qa_receipt.json of key " + bundle.Receipt.Signature.KeyID + ">"
	}
	return actions.JSONResult(out)
}

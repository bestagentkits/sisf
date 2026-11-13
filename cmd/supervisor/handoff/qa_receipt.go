package handoff

import (
	"context"
	"encoding/json"
	"strconv"

	iqa "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// SupervisorNamespace is where the supervisor keeps what it decided, such
// as the outcome of each evaluator attempt. Only the control zone reads or
// writes it.
const SupervisorNamespace = "supervisor"

// QAResultKey names the outcome of one evaluator attempt of a run.
func QAResultKey(run core.RunID, attempt int) string {
	return "qa/" + string(run) + "/" + strconv.Itoa(attempt)
}

// ReadQAReceipt reads the signed QA receipt of one evaluator attempt and
// the delegation of the key that signed it, as the supervisor stored them.
// NOT_FOUND means the attempt was not judged yet or ended without a
// receipt, as a candidate that never finished building does. A bundle the
// supervisor stored before it kept delegations has none; the delegation is
// then the Owner's to supply. Nothing here is trusted: the bundle is
// checked with the root public key alone (iqa.VerifyReceiptBundle).
func ReadQAReceipt(ctx context.Context, kv state.KV, run core.RunID, attempt int) (iqa.ReceiptBundle, error) {
	entry, err := kv.GetKV(ctx, SupervisorNamespace, QAResultKey(run, attempt))
	if err != nil {
		return iqa.ReceiptBundle{}, err
	}
	var b iqa.ReceiptBundle
	if err := json.Unmarshal(entry.Value, &b); err != nil {
		return iqa.ReceiptBundle{}, core.NewError(core.CodeTampered, "the stored QA result "+QAResultKey(run, attempt)+" cannot be read")
	}
	if b.Receipt.Signature.IsZero() {
		return iqa.ReceiptBundle{}, core.NewError(core.CodeNotFound, "QA signed no receipt for attempt "+strconv.Itoa(attempt)+" of run "+string(run))
	}
	return b, nil
}

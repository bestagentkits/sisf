package runactions_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/actions/runactions"
	iqa "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/core"
)

// receipts holds signed receipts by run and attempt.
type receipts map[string]iqa.ReceiptBundle

func (r receipts) QAReceipt(_ context.Context, run core.RunID, attempt int) (iqa.ReceiptBundle, string, error) {
	key := "qa/" + string(run) + "/" + strconv.Itoa(attempt)
	b, ok := r[key]
	if !ok {
		return iqa.ReceiptBundle{}, "", core.NewError(core.CodeNotFound, "QA signed no receipt for this attempt")
	}
	return b, "supervisor:" + key, nil
}

func bundle(run core.RunID, attempt int, withDelegation bool) iqa.ReceiptBundle {
	b := iqa.ReceiptBundle{Receipt: core.QAReceipt{RunID: run, EvaluatorAttempt: attempt, Score: 90,
		Signature: core.Signature{KeyID: "ed25519:qa-key", Algorithm: "ed25519", Value: []byte{1}}}}
	if withDelegation {
		b.Delegation = &delegation.SignedDelegation{Delegation: delegation.Delegation{KeyID: "ed25519:qa-key"}}
	}
	return b
}

// runs.receipt hands out the stored receipt of the issue's current
// attempt, or of the attempt asked for, with its key's delegation and the
// offline command that checks it. It vouches for nothing itself: a receipt
// stored without a delegation says that one must be supplied.
func TestRunsReceiptHandsOutTheSignedReceipt(t *testing.T) {
	t.Parallel()
	current := record(7, core.StateReleaseCandidate, "run-7", 0)
	current.Attempt = 2
	w := world{records: records{issueRef(7): current, issueRef(8): record(8, core.StateReady, "", 0)}, runs: runs{},
		receipts: receipts{"qa/run-7/2": bundle("run-7", 2, true), "qa/run-7/1": bundle("run-7", 1, false)}}
	a := w.action(t, runactions.ActionRunsReceipt)

	got, err := call[runactions.ReceiptExport](t, a, `{"issue":"acme/app#7"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempt != 2 || got.Receipt.EvaluatorAttempt != 2 || got.Delegation == nil || got.Ref != "supervisor:qa/run-7/2" || got.FactoryID != "acme-app" {
		t.Fatalf("got %+v", got)
	}
	for _, want := range []string{"sisf receipt verify", "--factory acme-app", "--issue acme/app#7", "--run run-7"} {
		if !strings.Contains(got.VerifyWith, want) {
			t.Errorf("verify_with %q lacks %q", got.VerifyWith, want)
		}
	}
	older, err := call[runactions.ReceiptExport](t, a, `{"issue":"acme/app#7","attempt":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if older.Delegation != nil || !strings.Contains(older.VerifyWith, "--delegation") {
		t.Errorf("a receipt without a delegation does not ask for one: %+v", older)
	}
	for in, code := range map[string]core.ErrorCode{
		`{"issue":"acme/app#7","attempt":3}`:  core.CodeNotFound,
		`{"issue":"acme/app#8"}`:              core.CodeNotFound,
		`{"issue":"acme/app#7","attempt":-1}`: core.CodeInvalidArgument,
		`{}`:                                  core.CodeInvalidArgument,
	} {
		if _, err := call[runactions.ReceiptExport](t, a, in); !core.HasCode(err, code) {
			t.Errorf("%s: want %s, got %v", in, code, err)
		}
	}
	w.receipts = nil
	if _, err := call[runactions.ReceiptExport](t, w.action(t, runactions.ActionRunsReceipt), `{"issue":"acme/app#7"}`); !core.HasCode(err, core.CodeNotSupported) {
		t.Errorf("without receipts: %v", err)
	}
}

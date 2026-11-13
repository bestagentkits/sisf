//go:build unix

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/bestagentkits/sisf/internal/actions/runactions"
	iqa "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The receipt QA signed for a run can be checked by someone who holds only
// what runs.receipt hands out and the Owner's root public key: it verifies,
// and it is refused for another run.
func TestAQAReceiptIsVerifiableWithTheRootPublicKeyAlone(t *testing.T) {
	t.Parallel()
	f, number := greetingIssue(t)
	f.Cycle(3)
	wantSurfaces(t, f, number, core.StateClosed)

	owner := f.SignInOwner()
	input, _ := json.Marshal(map[string]any{"input": map[string]string{"issue": f.Issue(number).String()}})
	resp := owner.Do(http.MethodPost, "/api/v1/actions/"+runactions.ActionRunsReceipt, bytes.NewReader(input),
		http.Header{"Content-Type": {"application/json"}, "X-Csrf-Token": {owner.CSRF()}})
	var out struct {
		Output runactions.ReceiptExport `json:"output"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("runs.receipt answered %d: %v", resp.StatusCode, err)
	}
	if out.Output.Delegation == nil {
		t.Fatalf("runs.receipt hands out no delegation: %+v", out.Output)
	}

	// Only the bundle crosses to the outside party: re-encode it as they
	// would receive it.
	raw, err := json.Marshal(out.Output.ReceiptBundle)
	if err != nil {
		t.Fatal(err)
	}
	var bundle iqa.ReceiptBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	record := f.Record(number)
	want := iqa.ReceiptClaim{FactoryID: f.Manifest.FactoryID, IssueID: f.Issue(number), RunID: record.RunID}
	check, err := iqa.VerifyReceiptBundle(context.Background(), f.Trust.Root, bundle, want, nil, f.Clock.Now())
	if err != nil {
		t.Fatalf("the stored receipt does not verify with the root key: %v", err)
	}
	if !check.HardGatesPassed || check.Receipt.RunID != record.RunID {
		t.Errorf("the verified receipt: %+v", check)
	}

	other := want
	other.RunID = core.RunID(string(record.RunID) + "x")
	if _, err := iqa.VerifyReceiptBundle(context.Background(), f.Trust.Root, bundle, other, nil, f.Clock.Now()); !core.HasCode(err, core.CodeGuardFailed) {
		t.Errorf("the receipt for another run was not refused: %v", err)
	}
}

package core

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A second commit and digest, for fields that must differ from the fixture.
const (
	otherSHA    = CommitSHA("fedcba9876543210fedcba9876543210fedcba98")
	otherDigest = Digest("sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")
)

// receiptMutations changes each field of a receipt in turn, keyed by the Go
// field name so a field added later cannot be left out of the payload unseen.
var receiptMutations = map[string]func(*QAReceipt){
	"FactoryID":           func(r *QAReceipt) { r.FactoryID = "other-factory" },
	"IssueID":             func(r *QAReceipt) { r.IssueID.Number++ },
	"RunID":               func(r *QAReceipt) { r.RunID = "run-0002" },
	"CandidateSHA":        func(r *QAReceipt) { r.CandidateSHA = otherSHA },
	"CandidateDigest":     func(r *QAReceipt) { r.CandidateDigest = otherDigest },
	"SuiteDigest":         func(r *QAReceipt) { r.SuiteDigest = otherDigest },
	"BaselineSHA":         func(r *QAReceipt) { r.BaselineSHA = otherSHA },
	"PolicySHA":           func(r *QAReceipt) { r.PolicySHA = otherSHA },
	"QAAgentIdentity":     func(r *QAReceipt) { r.QAAgentIdentity = "agent:qa-2" },
	"CodingAgentIdentity": func(r *QAReceipt) { r.CodingAgentIdentity = "agent:coder-2" },
	"CodingModelRoute":    func(r *QAReceipt) { r.CodingModelRoute.Model = "large" },
	"QAModelRoute":        func(r *QAReceipt) { r.QAModelRoute.Provider = "local" },
	"SameModelRoute":      func(r *QAReceipt) { r.SameModelRoute = !r.SameModelRoute },
	"Checks":              func(r *QAReceipt) { r.Checks[0].Passed = !r.Checks[0].Passed },
	"Score":               func(r *QAReceipt) { r.Score++ },
	"IssuedAt":            func(r *QAReceipt) { r.IssuedAt = r.IssuedAt.Add(time.Nanosecond) },
	"ExpiresAt":           func(r *QAReceipt) { r.ExpiresAt = r.ExpiresAt.Add(time.Nanosecond) },
	"EvaluatorAttempt":    func(r *QAReceipt) { r.EvaluatorAttempt++ },
}

func TestChangingAnyBoundReceiptFieldChangesTheSigningPayload(t *testing.T) {
	t.Parallel()
	base := validReceipt().SigningPayload()
	fields := reflect.TypeOf(QAReceipt{})
	for i := range fields.NumField() {
		name := fields.Field(i).Name
		if name == "Signature" {
			continue
		}
		mutate, ok := receiptMutations[name]
		if !ok {
			t.Errorf("receipt field %s has no case proving the signature covers it", name)
			continue
		}
		changed := validReceipt()
		mutate(&changed)
		if bytes.Equal(base, changed.SigningPayload()) {
			t.Errorf("changing %s left the signing payload unchanged", name)
		}
	}
}

func TestEveryPartOfARouteAndACheckIsBoundByTheReceiptSignature(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*QAReceipt){
		"issue owner":             func(r *QAReceipt) { r.IssueID.Owner = "other" },
		"issue repository":        func(r *QAReceipt) { r.IssueID.Repo = "other" },
		"coding route provider":   func(r *QAReceipt) { r.CodingModelRoute.Provider = "local" },
		"coding route effort":     func(r *QAReceipt) { r.CodingModelRoute.Effort = "high" },
		"qa route model":          func(r *QAReceipt) { r.QAModelRoute.Model = "large" },
		"qa route effort":         func(r *QAReceipt) { r.QAModelRoute.Effort = "high" },
		"check name":              func(r *QAReceipt) { r.Checks[0].Name = "secrets" },
		"check kind":              func(r *QAReceipt) { r.Checks[0].Kind = CheckRubric },
		"check points":            func(r *QAReceipt) { r.Checks[1].Points-- },
		"check maximum":           func(r *QAReceipt) { r.Checks[1].MaxPoints++ },
		"check detail":            func(r *QAReceipt) { r.Checks[0].Detail = "flaky" },
		"check evidence":          func(r *QAReceipt) { r.Checks[0].EvidenceRef = "logs/other.txt" },
		"a check was dropped":     func(r *QAReceipt) { r.Checks = r.Checks[:1] },
		"a check was added":       func(r *QAReceipt) { r.Checks = append(r.Checks, QACheck{Name: "secrets", Kind: CheckHardGate}) },
		"the checks were swapped": func(r *QAReceipt) { r.Checks[0], r.Checks[1] = r.Checks[1], r.Checks[0] },
	}
	base := validReceipt().SigningPayload()
	for name, mutate := range cases {
		changed := validReceipt()
		mutate(&changed)
		if bytes.Equal(base, changed.SigningPayload()) {
			t.Errorf("%s: the signing payload did not change", name)
		}
	}
}

func TestTheReceiptPayloadIsStableAndLeavesTheSignatureOut(t *testing.T) {
	t.Parallel()
	receipt := validReceipt()
	base := receipt.SigningPayload()
	if !bytes.Equal(base, validReceipt().SigningPayload()) {
		t.Fatal("two equal receipts produced different payloads")
	}
	// The field list changed when the factory was bound, so the version did
	// too: a signature made over the earlier list never verifies.
	if !strings.HasPrefix(string(base), "sisf-signed-record\ndomain=15:sisf.qa_receipt\nversion=1:2\nfactory_id=12:acme-factory\n") {
		t.Fatalf("payload does not open with the receipt domain and version: %q", base)
	}
	receipt.Signature = Signature{KeyID: "another-key", Algorithm: "ed25519", Value: []byte{9, 9}}
	if !bytes.Equal(base, receipt.SigningPayload()) {
		t.Fatal("the payload depends on the signature it is signed with")
	}
	receipt.IssuedAt = receipt.IssuedAt.In(time.FixedZone("ICT", 7*60*60))
	if !bytes.Equal(base, receipt.SigningPayload()) {
		t.Fatal("the payload depends on the zone an instant is expressed in")
	}
}

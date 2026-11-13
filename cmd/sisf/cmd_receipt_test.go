package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	iqa "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// receiptFiles is a signed receipt bundle and the root public key, written
// to files as an auditor receives them.
type receiptFiles struct {
	dir, rootKey, otherRootKey, bundle string
	receipt                            core.QAReceipt
	delegation                         delegation.SignedDelegation
}

// writeReceiptFiles signs a receipt for run-1 of acme/app#7 with a key the
// root delegated, with fresh keys made for the test.
func writeReceiptFiles(t *testing.T) receiptFiles {
	t.Helper()
	ctx := context.Background()
	issued := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root, err := signing.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	key, err := delegation.Issue(ctx, root, delegation.IssueRequest{FactoryID: "acme-app", Purpose: delegation.PurposeQAReceipt,
		Holder: delegation.HolderSupervisor, NotBefore: issued.Add(-time.Minute), Lifetime: delegation.MaxLifetime}, nil)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := delegation.NewChainVerifier(delegation.VerifierConfig{FactoryID: "acme-app", Root: pub, Clock: sqlstoretest.NewClock(issued)})
	if err != nil {
		t.Fatal(err)
	}
	signer, err := delegation.Load(ctx, verifier, delegation.LoadRequest{Holder: delegation.HolderSupervisor, Delegation: key.Delegation, PrivateKey: key.PrivateKey})
	if err != nil {
		t.Fatal(err)
	}
	r := core.QAReceipt{
		FactoryID: "acme-app", IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 7}, RunID: "run-1",
		CandidateSHA: core.CommitSHA(strings.Repeat("c", 40)), CandidateDigest: core.Digest("sha256:" + strings.Repeat("1", 64)),
		SuiteDigest: core.Digest("sha256:" + strings.Repeat("3", 64)), BaselineSHA: core.CommitSHA(strings.Repeat("b", 40)),
		PolicySHA: core.CommitSHA(strings.Repeat("d", 40)), QAAgentIdentity: "agent:qa", CodingAgentIdentity: "agent:coder",
		CodingModelRoute: core.ModelRoute{Provider: "anthropic", Model: "mid"}, QAModelRoute: core.ModelRoute{Provider: "openai", Model: "mid"},
		Checks: []core.QACheck{{Name: "tests", Kind: core.CheckHardGate, Passed: true},
			{Name: "correctness", Kind: core.CheckRubric, Passed: true, Points: 28, MaxPoints: 30}},
		Score: 28, IssuedAt: issued, ExpiresAt: issued.Add(24 * time.Hour), EvaluatorAttempt: 1,
	}
	if r.Signature, err = signer.Sign(ctx, r.SigningPayload()); err != nil {
		t.Fatal(err)
	}
	f := receiptFiles{dir: t.TempDir(), receipt: r, delegation: key.Delegation}
	f.rootKey = f.write(t, "root.pub", []byte(signing.EncodePublicKey(pub)))
	f.otherRootKey = f.write(t, "other-root.pub", []byte(signing.EncodePublicKey(otherPub)))
	f.bundle = f.writeBundle(t, "bundle.json", iqa.ReceiptBundle{Receipt: r, Delegation: &f.delegation}, false)
	return f
}

func (f receiptFiles) write(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(f.dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeBundle writes a bundle, wrapped as sisf call prints an action's
// output when wrapped is set.
func (f receiptFiles) writeBundle(t *testing.T, name string, b iqa.ReceiptBundle, wrapped bool) string {
	t.Helper()
	var v any = b
	if wrapped {
		v = map[string]any{"output": b}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return f.write(t, name, raw)
}

// claimArgs names what the receipt must be about.
func claimArgs(f receiptFiles, rootKey, run string) []string {
	return []string{"receipt", "verify", "--root-key", rootKey, "--factory", "acme-app", "--issue", "acme/app#7", "--run", run}
}

// The receipt is verified with the root public key alone: the report names
// the root key, the signing key and its window and the signed fields, and
// says the revocation check was not made. The output of sisf call, a bare
// receipt with its delegation given apart and the JSON form are accepted.
func TestReceiptVerifyAcceptsAGenuineReceipt(t *testing.T) {
	t.Parallel()
	f := writeReceiptFiles(t)
	bare, err := json.Marshal(f.receipt)
	if err != nil {
		t.Fatal(err)
	}
	delegationJSON, err := json.Marshal(f.delegation)
	if err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"bundle":           append(claimArgs(f, f.rootKey, "run-1"), "--commit", strings.Repeat("c", 40), f.bundle),
		"sisf call output": append(claimArgs(f, f.rootKey, "run-1"), f.writeBundle(t, "call.json", iqa.ReceiptBundle{Receipt: f.receipt, Delegation: &f.delegation}, true)),
		"bare receipt": append(claimArgs(f, f.rootKey, "run-1"), "--delegation", f.write(t, "delegation.json", delegationJSON),
			f.write(t, "receipt.json", bare)),
	} {
		status, stdout, stderr := runWithOutput(t, args...)
		if status != exitOK {
			t.Fatalf("%s: exit %d: %s", name, status, stderr)
		}
		for _, want := range []string{"QA receipt verified", f.delegation.Delegation.KeyID, f.delegation.Delegation.RootKeyID,
			"qa_receipt held by supervisor", "not checked", "run-1", "acme/app#7", strings.Repeat("c", 40), "correctness", "28/30"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("%s: the report does not show %q:\n%s", name, want, stdout)
			}
		}
	}
	status, stdout, stderr := runWithOutput(t, append(claimArgs(f, f.rootKey, "run-1"), "--json", f.bundle)...)
	var out struct {
		Verdict string `json:"verdict"`
		KeyID   string `json:"key_id"`
	}
	if status != exitOK || json.Unmarshal([]byte(stdout), &out) != nil || out.Verdict != "verified" || out.KeyID != f.delegation.Delegation.KeyID {
		t.Fatalf("--json: exit %d, %q, %s", status, stdout, stderr)
	}
}

// A tampered receipt, the wrong root key and a receipt for another run are
// each refused with exit status 1; a call that does not say what the
// receipt must be about is a usage error.
func TestReceiptVerifyRefusesTamperingAnotherKeyAndAnotherRun(t *testing.T) {
	t.Parallel()
	f := writeReceiptFiles(t)
	tampered := f.receipt
	tampered.Score = 30
	tamperedFile := f.writeBundle(t, "tampered.json", iqa.ReceiptBundle{Receipt: tampered, Delegation: &f.delegation}, false)
	for name, c := range map[string]struct {
		args   []string
		status int
		says   string
	}{
		"tampered receipt": {append(claimArgs(f, f.rootKey, "run-1"), tamperedFile), exitFailure, "SIGNATURE_INVALID"},
		"wrong root key":   {append(claimArgs(f, f.otherRootKey, "run-1"), f.bundle), exitFailure, "SIGNATURE_INVALID"},
		"another run":      {append(claimArgs(f, f.rootKey, "run-2"), f.bundle), exitFailure, "another issue or run"},
		"another commit":   {append(claimArgs(f, f.rootKey, "run-1"), "--commit", strings.Repeat("e", 40), f.bundle), exitFailure, "another commit"},
		"no root key":      {[]string{"receipt", "verify", "--factory", "acme-app", "--issue", "acme/app#7", "--run", "run-1", f.bundle}, exitUsage, "--root-key"},
		"no run":           {[]string{"receipt", "verify", "--root-key", f.rootKey, "--factory", "acme-app", "--issue", "acme/app#7", f.bundle}, exitUsage, "--run"},
	} {
		status, stdout, stderr := runWithOutput(t, c.args...)
		if status != c.status {
			t.Errorf("%s: exit %d, want %d; stdout %q, stderr %q", name, status, c.status, stdout, stderr)
		}
		if !strings.Contains(stderr, c.says) {
			t.Errorf("%s: the error does not say %q: %s", name, c.says, stderr)
		}
		if strings.Contains(stdout, "verified") {
			t.Errorf("%s: a refused receipt was reported verified: %s", name, stdout)
		}
	}
}

package qa_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	evaluator "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/core"
)

// bundleEnv is a signed receipt with the delegation of its key, as an
// outside party receives it, and the root public key it is checked with.
type bundleEnv struct {
	*env
	rootPub ed25519.PublicKey
	bundle  evaluator.ReceiptBundle
	key     delegation.SignedDelegation
}

// newBundleEnv evaluates a candidate and bundles its receipt with the
// delegation of the key that signed it.
func newBundleEnv(t *testing.T) *bundleEnv {
	t.Helper()
	e := newEnv(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the environment's keys by ones whose root public key the
	// test keeps.
	root, err := signing.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := delegation.NewChainVerifier(delegation.VerifierConfig{FactoryID: factory, Root: pub, Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	key, err := delegation.Issue(context.Background(), root, delegation.IssueRequest{FactoryID: factory,
		Purpose: delegation.PurposeQAReceipt, Holder: delegation.HolderSupervisor, NotBefore: start, Lifetime: delegation.MaxLifetime}, nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := delegation.Load(context.Background(), verifier, delegation.LoadRequest{
		Holder: delegation.HolderSupervisor, Delegation: key.Delegation, PrivateKey: key.PrivateKey})
	if err != nil {
		t.Fatal(err)
	}
	e.root, e.verifier, e.signer, e.cfg.Signer, e.cfg.Verifier = root, verifier, signer, signer, verifier
	r, err := e.build(t).Evaluate(context.Background(), candidate())
	if err != nil {
		t.Fatal(err)
	}
	signed := key.Delegation
	return &bundleEnv{env: e, rootPub: pub, key: signed, bundle: evaluator.ReceiptBundle{Receipt: r, Delegation: &signed}}
}

// claim is what the outside party expects the receipt to be about.
func claim() evaluator.ReceiptClaim {
	return evaluator.ReceiptClaim{FactoryID: factory, IssueID: issue, RunID: "run-0001", CandidateSHA: candidateSHA}
}

// An outside party holding only the bundle and the root public key verifies
// the receipt: the key that signed it, the delegation behind that key and
// every signed field are reported. The key is judged as of the receipt's
// issue time, so a receipt stays verifiable after its week-long key has
// expired.
func TestAReceiptBundleVerifiesWithTheRootPublicKeyAlone(t *testing.T) {
	t.Parallel()
	b := newBundleEnv(t)
	for name, at := range map[string]time.Time{"now": b.clock.Now(), "after the key expired": start.Add(30 * 24 * time.Hour)} {
		got, err := evaluator.VerifyReceiptBundle(context.Background(), b.rootPub, b.bundle, claim(), nil, at)
		if err != nil {
			t.Fatalf("%s: a genuine receipt does not verify: %v", name, err)
		}
		switch {
		case got.KeyID != b.bundle.Receipt.Signature.KeyID || got.KeyID != b.key.Delegation.KeyID:
			t.Errorf("%s: reported key %q, want %q", name, got.KeyID, b.key.Delegation.KeyID)
		case got.RootKeyID != signing.KeyID(b.rootPub):
			t.Errorf("%s: reported root key %q", name, got.RootKeyID)
		case got.Purpose != delegation.PurposeQAReceipt || got.Holder != delegation.HolderSupervisor:
			t.Errorf("%s: reported purpose %s held by %s", name, got.Purpose, got.Holder)
		case !got.KeyNotBefore.Equal(b.key.Delegation.NotBefore) || !got.KeyNotAfter.Equal(b.key.Delegation.NotAfter) || !got.JudgedAt.Equal(b.bundle.Receipt.IssuedAt):
			t.Errorf("%s: reported window %s-%s judged at %s", name, got.KeyNotBefore, got.KeyNotAfter, got.JudgedAt)
		case got.Receipt.Score != b.bundle.Receipt.Score || got.Receipt.RunID != "run-0001" || !got.HardGatesPassed:
			t.Errorf("%s: reported signed fields %+v", name, got.Receipt)
		case got.RevocationSequence != 0:
			t.Errorf("%s: claims a revocation list was checked", name)
		}
		if want := name != "now"; got.Expired != want {
			t.Errorf("%s: expired = %v, want %v", name, got.Expired, want)
		}
	}
}

// A receipt changed after it was signed is refused, whatever field changed.
func TestATamperedReceiptIsRefused(t *testing.T) {
	t.Parallel()
	b := newBundleEnv(t)
	for name, tamper := range map[string]func(*core.QAReceipt){
		"score raised":       func(r *core.QAReceipt) { r.Score-- },
		"gate flipped":       func(r *core.QAReceipt) { r.Checks[0].Passed = !r.Checks[0].Passed },
		"commit changed":     func(r *core.QAReceipt) { r.CandidateSHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" },
		"run moved":          func(r *core.QAReceipt) { r.RunID = "run-0002" },
		"expiry pushed out":  func(r *core.QAReceipt) { r.ExpiresAt = r.ExpiresAt.Add(time.Hour) },
		"signature replaced": func(r *core.QAReceipt) { r.Signature.Value = append([]byte{}, make([]byte, ed25519.SignatureSize)...) },
	} {
		bundle := b.bundle
		bundle.Receipt.Checks = append([]core.QACheck(nil), b.bundle.Receipt.Checks...)
		tamper(&bundle.Receipt)
		want := claim()
		want.RunID, want.CandidateSHA = bundle.Receipt.RunID, bundle.Receipt.CandidateSHA
		_, err := evaluator.VerifyReceiptBundle(context.Background(), b.rootPub, bundle, want, nil, b.clock.Now())
		if !core.HasCode(err, core.CodeSignatureInvalid) {
			t.Errorf("%s: want %s, got %v", name, core.CodeSignatureInvalid, err)
		}
	}
}

// The receipt is refused against another root key, and with a delegation
// that is not the one of its signing key, that names another factory, that
// is for another kind of record or whose key the Owner revoked. A bundle
// without a delegation proves nothing.
func TestAReceiptIsRefusedWithAnotherKey(t *testing.T) {
	t.Parallel()
	b := newBundleEnv(t)
	ctx := context.Background()
	otherRoot, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(f core.FactoryID, p delegation.Purpose) delegation.SignedDelegation {
		key, err := delegation.Issue(ctx, b.root, delegation.IssueRequest{FactoryID: f, Purpose: p,
			Holder: delegation.HolderSupervisor, NotBefore: start, Lifetime: time.Hour}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return key.Delegation
	}
	revoked, err := delegation.SignRevocations(ctx, b.root, delegation.RevocationList{
		FactoryID: factory, Sequence: 3, IssuedAt: b.clock.Now(), KeyIDs: []string{b.key.Delegation.KeyID}})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		root        ed25519.PublicKey
		delegation  *delegation.SignedDelegation
		revocations *delegation.SignedRevocationList
		code        core.ErrorCode
	}{
		"another root key":           {otherRoot, b.bundle.Delegation, nil, core.CodeSignatureInvalid},
		"another key's delegation":   {b.rootPub, ptr(issue(factory, delegation.PurposeQAReceipt)), nil, core.CodeSignatureInvalid},
		"another factory's key":      {b.rootPub, ptr(issue("other-factory", delegation.PurposeQAReceipt)), nil, core.CodeSignatureInvalid},
		"a deploy artifact key":      {b.rootPub, ptr(issue(factory, delegation.PurposeDeployArtifact)), nil, core.CodeSignatureInvalid},
		"the key revoked":            {b.rootPub, b.bundle.Delegation, &revoked, core.CodeSignatureInvalid},
		"no delegation":              {b.rootPub, nil, nil, core.CodeInvalidArgument},
		"a forged delegation window": {b.rootPub, ptr(widened(b.key)), nil, core.CodeSignatureInvalid},
	} {
		bundle := b.bundle
		bundle.Delegation = c.delegation
		_, err := evaluator.VerifyReceiptBundle(ctx, c.root, bundle, claim(), c.revocations, b.clock.Now())
		if !core.HasCode(err, c.code) {
			t.Errorf("%s: want %s, got %v", name, c.code, err)
		}
	}
}

// A genuine receipt is refused when it is about another factory, issue,
// run or commit than the one the outside party asked about.
func TestAReceiptForAnotherRunIsRefused(t *testing.T) {
	t.Parallel()
	b := newBundleEnv(t)
	for name, c := range map[string]struct {
		mutate func(*evaluator.ReceiptClaim)
		code   core.ErrorCode
	}{
		"another run":     {func(w *evaluator.ReceiptClaim) { w.RunID = "run-0002" }, core.CodeGuardFailed},
		"another issue":   {func(w *evaluator.ReceiptClaim) { w.IssueID.Number = 8 }, core.CodeGuardFailed},
		"another commit":  {func(w *evaluator.ReceiptClaim) { w.CandidateSHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" }, core.CodeGuardFailed},
		"another factory": {func(w *evaluator.ReceiptClaim) { w.FactoryID = "other-factory" }, core.CodeSignatureInvalid},
		"no run named":    {func(w *evaluator.ReceiptClaim) { w.RunID = "" }, core.CodeInvalidArgument},
		"no issue named":  {func(w *evaluator.ReceiptClaim) { w.IssueID = core.IssueRef{} }, core.CodeInvalidArgument},
	} {
		want := claim()
		c.mutate(&want)
		_, err := evaluator.VerifyReceiptBundle(context.Background(), b.rootPub, b.bundle, want, nil, b.clock.Now())
		if !core.HasCode(err, c.code) {
			t.Errorf("%s: want %s, got %v", name, c.code, err)
		}
	}
	any := claim()
	any.CandidateSHA = ""
	if _, err := evaluator.VerifyReceiptBundle(context.Background(), b.rootPub, b.bundle, any, nil, b.clock.Now()); err != nil {
		t.Errorf("naming no commit should check the rest: %v", err)
	}
}

// widened is the receipt key's delegation with a longer window and its
// root signature kept.
func widened(sd delegation.SignedDelegation) delegation.SignedDelegation {
	sd.Delegation.NotBefore = sd.Delegation.NotBefore.Add(-time.Hour)
	return sd
}

func ptr[T any](v T) *T { return &v }

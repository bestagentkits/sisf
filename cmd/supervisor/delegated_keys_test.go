package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// trustVolume is a supervisor-only directory and a root key.
func trustVolume(t *testing.T) (string, ed25519.PublicKey, core.Signer) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "trust")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root, err := signing.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	return dir, pub, root
}

// Keys the supervisor issued are loaded again without the root key, a key
// near its end is replaced, and a delegation edited on the volume is refused.
func TestDelegatedKeysAreIssuedReloadedRenewedAndChecked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := sqlstoretest.NewClock(time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC))
	dir, pub, root := trustVolume(t)

	issued, err := loadSupervisorKeys(ctx, dir, testFactory, pub, root, clock, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadSupervisorKeys(ctx, dir, testFactory, pub, nil, clock, slog.New(slog.DiscardHandler))
	if err != nil || reloaded.QA.KeyID() != issued.QA.KeyID() || reloaded.Artifact.KeyID() != issued.Artifact.KeyID() {
		t.Fatalf("the issued keys were not reloaded: %v", err)
	}
	if issued.QA.KeyID() == issued.Artifact.KeyID() {
		t.Fatal("one key signs two kinds of record")
	}

	clock.Advance(delegation.MaxLifetime - renewBefore/2)
	renewed, err := loadSupervisorKeys(ctx, dir, testFactory, pub, root, clock, slog.New(slog.DiscardHandler))
	if err != nil || renewed.QA.KeyID() == issued.QA.KeyID() {
		t.Fatalf("a key about to expire was not replaced: %v", err)
	}

	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadSupervisorKeys(ctx, dir, testFactory, other, nil, clock, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("keys delegated by another root were trusted")
	}
	path := filepath.Join(dir, delegationFile(delegation.PurposeQAReceipt))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var signed delegation.SignedDelegation
	if err := json.Unmarshal(raw, &signed); err != nil {
		t.Fatal(err)
	}
	signed.Delegation.NotBefore = signed.Delegation.NotBefore.Add(time.Hour)
	edited, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSupervisorKeys(ctx, dir, testFactory, pub, nil, clock, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("an edited delegation was trusted")
	}
}

// Without the root key and without keys on the volume nothing is loaded.
func TestMissingDelegatedKeysAreNotFound(t *testing.T) {
	t.Parallel()
	dir, pub, _ := trustVolume(t)
	_, err := loadSupervisorKeys(context.Background(), dir, testFactory, pub, nil, core.SystemClock{}, slog.New(slog.DiscardHandler))
	if !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("got %v, want NOT_FOUND", err)
	}
}

// A receipt signed with a key that expired since still verifies as of an
// instant the key was valid, and not as of one after its end.
func TestASignatureIsJudgedAsOfAnEarlierInstant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	start := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	clock := sqlstoretest.NewClock(start)
	dir, pub, root := trustVolume(t)
	keys, err := loadSupervisorKeys(ctx, dir, testFactory, pub, root, clock, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	payload := core.NewPayloadBuilder("sisf.qa_receipt", 1).Bytes()
	sig, err := keys.QA.Sign(ctx, payload)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * delegation.MaxLifetime)
	if err := keys.Verifier.Verify(ctx, payload, sig); err == nil {
		t.Fatal("an expired key still verifies now; the test does not exercise expiry")
	}
	then, err := keys.VerifierAt(start.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := then.Verify(ctx, payload, sig); err != nil {
		t.Fatalf("the signature does not verify as of when the key was valid: %v", err)
	}
	later, err := keys.VerifierAt(start.Add(2 * delegation.MaxLifetime))
	if err != nil {
		t.Fatal(err)
	}
	if err := later.Verify(ctx, payload, sig); err == nil {
		t.Fatal("the signature verifies as of after the key's end")
	}
}

// A key revoked since the instant a signature is judged as of is refused:
// the pinned verifier forgives expiry, never a revocation.
func TestARevokedKeyIsRefusedAsOfAnEarlierInstant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	start := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	clock := sqlstoretest.NewClock(start)
	dir, pub, root := trustVolume(t)
	keys, err := loadSupervisorKeys(ctx, dir, testFactory, pub, root, clock, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	payload := core.NewPayloadBuilder("sisf.qa_receipt", 1).Bytes()
	sig, err := keys.QA.Sign(ctx, payload)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)
	list, err := delegation.SignRevocations(ctx, root, delegation.RevocationList{
		FactoryID: testFactory, Sequence: 1, IssuedAt: clock.Now(), KeyIDs: []string{keys.QA.KeyID()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Verifier.ApplyRevocations(ctx, list); err != nil {
		t.Fatal(err)
	}
	then, err := keys.VerifierAt(start.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := then.Verify(ctx, payload, sig); !core.HasCode(err, core.CodeSignatureInvalid) {
		t.Fatalf("a signature by a revoked key verifies as of before the revocation: %v", err)
	}
}

// The supervisor hands out the root-signed delegation of the key that signed
// a QA receipt, so the receipt can be checked with the root key alone, and
// none for a key it never trusted.
func TestTheDelegationOfAQAKeyIsHandedOut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := sqlstoretest.NewClock(time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC))
	dir, pub, root := trustVolume(t)
	keys, err := loadSupervisorKeys(ctx, dir, testFactory, pub, root, clock, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	d, ok := keys.Delegation(keys.QA.KeyID())
	if !ok || d.Delegation.KeyID != keys.QA.KeyID() || d.Delegation.Purpose != delegation.PurposeQAReceipt {
		t.Fatalf("the QA key's delegation: %+v, %t", d.Delegation, ok)
	}
	verifier, err := delegation.NewChainVerifier(delegation.VerifierConfig{FactoryID: testFactory, Root: pub, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Trust(ctx, d); err != nil {
		t.Fatalf("the handed-out delegation is not vouched for by the root key: %v", err)
	}
	if _, ok := keys.Delegation("ed25519:unknown"); ok {
		t.Fatal("a delegation was handed out for a key the supervisor never trusted")
	}
}

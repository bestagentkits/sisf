package delegation

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestDelegatedKeySignsAndVerifiesThroughTheChain(t *testing.T) {
	t.Parallel()
	c := newChain(t)
	signer := c.load(t, c.issue(t, PurposeQAReceipt, HolderSupervisor))
	sig, err := signer.Sign(context.Background(), receiptPayload())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := c.verifier.Verify(context.Background(), receiptPayload(), sig); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	var _ core.Signer = signer
	var _ core.Verifier = c.verifier
}

func TestRevokedKeyIsRefusedWithoutReplacingTheRoot(t *testing.T) {
	t.Parallel()
	c := newChain(t)
	ctx := context.Background()
	key := c.issue(t, PurposeDeployArtifact, HolderSupervisor)
	signer := c.load(t, key)
	sig, err := signer.Sign(ctx, artifactPayload())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	c.revoke(t, 1, signer.KeyID())

	wantCode(t, c.verifier.Verify(ctx, artifactPayload(), sig), core.CodeSignatureInvalid)
	_, err = signer.Sign(ctx, artifactPayload())
	wantCode(t, err, core.CodePermissionDenied)
	wantCode(t, c.verifier.Trust(ctx, key.Delegation), core.CodeSignatureInvalid)
	_, err = Load(ctx, c.verifier, LoadRequest{Holder: HolderSupervisor, Delegation: key.Delegation, PrivateKey: key.PrivateKey})
	wantCode(t, err, core.CodeSignatureInvalid)
	if !c.verifier.Revoked(signer.KeyID()) {
		t.Fatal("Revoked reports the key as live")
	}

	// The same root issues a replacement that works at once.
	replacement := c.load(t, c.issue(t, PurposeDeployArtifact, HolderSupervisor))
	sig, err = replacement.Sign(ctx, artifactPayload())
	if err != nil {
		t.Fatalf("Sign with replacement: %v", err)
	}
	if err := c.verifier.Verify(ctx, artifactPayload(), sig); err != nil {
		t.Fatalf("Verify with replacement: %v", err)
	}
}

func TestRevocationCannotBeUndoneByAnOlderOrShorterList(t *testing.T) {
	t.Parallel()
	c := newChain(t)
	ctx := context.Background()
	revokedKey := c.issue(t, PurposeApproval, HolderApprovalService)
	revokedID := revokedKey.Delegation.Delegation.KeyID
	c.revoke(t, 2, revokedID)

	older, err := SignRevocations(ctx, c.root, RevocationList{FactoryID: testFactory, Sequence: 1, IssuedAt: c.clock.Now()})
	if err != nil {
		t.Fatalf("SignRevocations: %v", err)
	}
	wantCode(t, c.verifier.ApplyRevocations(ctx, older), core.CodeReplayed)

	// Applying the list in force again changes nothing.
	again, err := SignRevocations(ctx, c.root, RevocationList{FactoryID: testFactory, Sequence: 2, IssuedAt: c.clock.Now(), KeyIDs: []string{revokedID}})
	if err != nil {
		t.Fatalf("SignRevocations: %v", err)
	}
	if err := c.verifier.ApplyRevocations(ctx, again); err != nil {
		t.Fatalf("re-applying the list in force: %v", err)
	}

	// A newer list that no longer names the key does not bring it back.
	c.revoke(t, 3)
	if !c.verifier.Revoked(revokedID) {
		t.Fatal("a later list without the key un-revoked it")
	}
	wantCode(t, c.verifier.Trust(ctx, revokedKey.Delegation), core.CodeSignatureInvalid)
}

func TestRevocationListMustBeSignedByThisRoot(t *testing.T) {
	t.Parallel()
	c := newChain(t)
	ctx := context.Background()
	key := c.issue(t, PurposeApproval, HolderApprovalService)
	signer := c.load(t, key)
	otherRoot, _ := newRoot(t)

	forged, err := SignRevocations(ctx, otherRoot, RevocationList{FactoryID: testFactory, Sequence: 1, IssuedAt: c.clock.Now(), KeyIDs: []string{signer.KeyID()}})
	if err != nil {
		t.Fatalf("SignRevocations: %v", err)
	}
	wantCode(t, c.verifier.ApplyRevocations(ctx, forged), core.CodeSignatureInvalid)

	// Tampering with a genuine list breaks its signature.
	genuine, err := SignRevocations(ctx, c.root, RevocationList{FactoryID: testFactory, Sequence: 1, IssuedAt: c.clock.Now(), KeyIDs: []string{"ed25519:other"}})
	if err != nil {
		t.Fatalf("SignRevocations: %v", err)
	}
	genuine.List.Sequence = 9
	wantCode(t, c.verifier.ApplyRevocations(ctx, genuine), core.CodeSignatureInvalid)
	if c.verifier.Revoked(signer.KeyID()) {
		t.Fatal("a forged list revoked a key")
	}
}

func TestExpiredKeyIsRefused(t *testing.T) {
	t.Parallel()
	c := newChain(t)
	ctx := context.Background()
	signer := c.load(t, c.issue(t, PurposeQAReceipt, HolderSupervisor))
	sig, err := signer.Sign(ctx, receiptPayload())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	c.clock.Advance(25 * time.Hour)
	wantCode(t, c.verifier.Verify(ctx, receiptPayload(), sig), core.CodeSignatureInvalid)
	_, err = signer.Sign(ctx, receiptPayload())
	wantCode(t, err, core.CodePermissionDenied)
}

func TestExpiredKeyCannotBeLoaded(t *testing.T) {
	t.Parallel()
	c := newChain(t)
	key := c.issue(t, PurposeQAReceipt, HolderSupervisor)
	c.clock.Advance(24 * time.Hour)
	_, err := Load(context.Background(), c.verifier, LoadRequest{Holder: HolderSupervisor, Delegation: key.Delegation, PrivateKey: key.PrivateKey})
	wantCode(t, err, core.CodePermissionDenied)
}

func TestKeyUsedForTheWrongPurposeIsRefused(t *testing.T) {
	t.Parallel()
	c := newChain(t)
	ctx := context.Background()
	key := c.issue(t, PurposeApproval, HolderApprovalService)
	signer := c.load(t, key)

	for name, payload := range map[string][]byte{"qa receipt": receiptPayload(), "deploy artifact": artifactPayload()} {
		if _, err := signer.Sign(ctx, payload); !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("signing a %s with an approval key: error = %v, want PERMISSION_DENIED", name, err)
		}
		// A signature made with the raw key, bypassing the signer, is still refused.
		priv, err := signing.ParsePrivateKey(key.PrivateKey.Reveal())
		if err != nil {
			t.Fatalf("ParsePrivateKey: %v", err)
		}
		raw := core.Signature{KeyID: signer.KeyID(), Algorithm: signing.Algorithm, Value: ed25519.Sign(priv, payload)}
		if err := c.verifier.Verify(ctx, payload, raw); !core.HasCode(err, core.CodeSignatureInvalid) {
			t.Errorf("verifying a %s signed by an approval key: error = %v, want SIGNATURE_INVALID", name, err)
		}
	}
}

func TestDelegationSignedByAnotherRootIsRefused(t *testing.T) {
	t.Parallel()
	c := newChain(t)
	ctx := context.Background()
	otherRoot, _ := newRoot(t)
	key, err := Issue(ctx, otherRoot, IssueRequest{FactoryID: testFactory, Purpose: PurposeQAReceipt, Holder: HolderSupervisor, NotBefore: c.clock.Now(), Lifetime: time.Hour}, nil)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	wantCode(t, c.verifier.Trust(ctx, key.Delegation), core.CodeSignatureInvalid)

	// Claiming this factory's root key id over another root's signature fails too.
	key.Delegation.Delegation.RootKeyID = c.root.KeyID()
	key.Delegation.Signature.KeyID = c.root.KeyID()
	wantCode(t, c.verifier.Trust(ctx, key.Delegation), core.CodeSignatureInvalid)
	_, err = Load(ctx, c.verifier, LoadRequest{Holder: HolderSupervisor, Delegation: key.Delegation, PrivateKey: key.PrivateKey})
	wantCode(t, err, core.CodeSignatureInvalid)
}

func TestTamperedDelegationIsRefused(t *testing.T) {
	t.Parallel()
	attackerPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tamper := map[string]func(*Delegation){
		"extended window": func(d *Delegation) { d.NotAfter = d.NotAfter.Add(time.Hour) },
		"earlier start":   func(d *Delegation) { d.NotBefore = d.NotBefore.Add(-time.Hour) },
		"other purpose":   func(d *Delegation) { d.Purpose = PurposeDeployArtifact },
		"other factory":   func(d *Delegation) { d.FactoryID = "acme-other" },
		"swapped key": func(d *Delegation) {
			d.PublicKey = signing.EncodePublicKey(attackerPub)
			d.KeyID = signing.KeyID(attackerPub)
		},
		"missing signature": func(d *Delegation) {},
	}
	for name, change := range tamper {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newChain(t)
			key := c.issue(t, PurposeQAReceipt, HolderSupervisor)
			change(&key.Delegation.Delegation)
			if name == "missing signature" {
				key.Delegation.Signature = core.Signature{}
			}
			wantCode(t, c.verifier.Trust(context.Background(), key.Delegation), core.CodeSignatureInvalid)
		})
	}
}

func TestDelegationForAnotherFactoryIsRefused(t *testing.T) {
	t.Parallel()
	c := newChain(t)
	key, err := Issue(context.Background(), c.root, IssueRequest{FactoryID: "acme-other", Purpose: PurposeQAReceipt, Holder: HolderSupervisor, NotBefore: c.clock.Now(), Lifetime: time.Hour}, nil)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	wantCode(t, c.verifier.Trust(context.Background(), key.Delegation), core.CodeSignatureInvalid)
}

func TestRootSignedDelegationBeyondMaxLifetimeIsRefused(t *testing.T) {
	t.Parallel()
	c := newChain(t)
	ctx := context.Background()
	_, err := Issue(ctx, c.root, IssueRequest{FactoryID: testFactory, Purpose: PurposeQAReceipt, Holder: HolderSupervisor, NotBefore: c.clock.Now(), Lifetime: MaxLifetime + time.Second}, nil)
	wantCode(t, err, core.CodeInvalidArgument)

	// A long-lived delegation signed outside Issue is still not trusted.
	key := c.issue(t, PurposeQAReceipt, HolderSupervisor)
	d := key.Delegation.Delegation
	d.NotAfter = d.NotBefore.Add(MaxLifetime + time.Second)
	sig, err := c.root.Sign(ctx, d.SigningPayload())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	wantCode(t, c.verifier.Trust(ctx, SignedDelegation{Delegation: d, Signature: sig}), core.CodeInvalidArgument)
}

func TestClockSkewAtTheWindowBoundaries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	notBefore := start.Add(time.Hour)
	notAfter := notBefore.Add(6 * time.Hour)
	cases := []struct {
		name  string
		at    time.Time
		valid bool
	}{
		{"before start beyond the skew", notBefore.Add(-DefaultClockSkew - time.Nanosecond), false},
		{"before start within the skew", notBefore.Add(-DefaultClockSkew), true},
		{"at start", notBefore, true},
		{"last instant", notAfter.Add(-time.Nanosecond), true},
		{"at end", notAfter, false},
		{"end plus skew", notAfter.Add(DefaultClockSkew), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newChain(t)
			key, err := Issue(ctx, c.root, IssueRequest{FactoryID: testFactory, Purpose: PurposeQAReceipt, Holder: HolderSupervisor, NotBefore: notBefore, Lifetime: notAfter.Sub(notBefore)}, nil)
			if err != nil {
				t.Fatalf("Issue: %v", err)
			}
			if err := c.verifier.Trust(ctx, key.Delegation); err != nil {
				t.Fatalf("Trust: %v", err)
			}
			priv, err := signing.ParsePrivateKey(key.PrivateKey.Reveal())
			if err != nil {
				t.Fatalf("ParsePrivateKey: %v", err)
			}
			sig := core.Signature{KeyID: key.Delegation.Delegation.KeyID, Algorithm: signing.Algorithm, Value: ed25519.Sign(priv, receiptPayload())}
			c.clock.Set(tc.at)
			err = c.verifier.Verify(ctx, receiptPayload(), sig)
			if tc.valid && err != nil {
				t.Fatalf("Verify at %s: %v", tc.at, err)
			}
			if !tc.valid {
				wantCode(t, err, core.CodeSignatureInvalid)
			}
		})
	}
}

func TestOnlyTheIssuedTrustedHolderMayLoadAKey(t *testing.T) {
	t.Parallel()
	c := newChain(t)
	ctx := context.Background()
	key := c.issue(t, PurposeQAReceipt, HolderSupervisor)
	for _, holder := range []Holder{"agent", "candidate", "", HolderApprovalService} {
		_, err := Load(ctx, c.verifier, LoadRequest{Holder: holder, Delegation: key.Delegation, PrivateKey: key.PrivateKey})
		if !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("holder %q: error = %v, want PERMISSION_DENIED", holder, err)
		}
	}
	other := c.issue(t, PurposeQAReceipt, HolderSupervisor)
	_, err := Load(ctx, c.verifier, LoadRequest{Holder: HolderSupervisor, Delegation: key.Delegation, PrivateKey: other.PrivateKey})
	wantCode(t, err, core.CodeInvalidArgument)
}

func TestHolderMayHoldOnlyItsPurposes(t *testing.T) {
	t.Parallel()
	c := newChain(t)
	for _, pair := range []struct {
		holder  Holder
		purpose Purpose
	}{{HolderApprovalService, PurposeDeployArtifact}, {HolderApprovalService, PurposeQAReceipt}, {HolderSupervisor, PurposeApproval}} {
		_, err := Issue(context.Background(), c.root, IssueRequest{FactoryID: testFactory, Purpose: pair.purpose, Holder: pair.holder, NotBefore: c.clock.Now(), Lifetime: time.Hour}, nil)
		if !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s holding %s: error = %v, want INVALID_ARGUMENT", pair.holder, pair.purpose, err)
		}
	}
}

func TestPurposesCoverTheRealRecordPayloads(t *testing.T) {
	t.Parallel()
	payloads := map[Purpose][]byte{PurposeQAReceipt: receiptPayload(), PurposeApproval: approvalPayload(), PurposeDeployArtifact: artifactPayload()}
	for purpose, payload := range payloads {
		for other, otherPayload := range payloads {
			if got := purpose.Covers(otherPayload); got != (purpose == other) {
				t.Errorf("%s covers the %s payload = %v", purpose, other, got)
			}
		}
		if !purpose.Covers(payload) {
			t.Errorf("%s does not cover its own payload", purpose)
		}
	}
}

func TestLocalProfileReportsLowerAssurance(t *testing.T) {
	t.Parallel()
	_, pub := newRoot(t)
	offline, err := NewChainVerifier(VerifierConfig{FactoryID: testFactory, Root: pub, Clock: newFakeClock()})
	if err != nil {
		t.Fatalf("NewChainVerifier: %v", err)
	}
	local, err := NewChainVerifier(VerifierConfig{FactoryID: testFactory, Root: pub, Clock: newFakeClock(), RootKeyInFactoryStore: true})
	if err != nil {
		t.Fatalf("NewChainVerifier: %v", err)
	}
	if offline.Assurance().Lower() || offline.Assurance() != AssuranceRootOffline {
		t.Errorf("offline root assurance = %s", offline.Assurance())
	}
	if !local.Assurance().Lower() || local.Assurance() != AssuranceLower {
		t.Errorf("local profile assurance = %s", local.Assurance())
	}
}

func TestChainVerifierRefusesAMissingAnchor(t *testing.T) {
	t.Parallel()
	_, pub := newRoot(t)
	for name, cfg := range map[string]VerifierConfig{
		"no root":       {FactoryID: testFactory, Clock: newFakeClock()},
		"no clock":      {FactoryID: testFactory, Root: pub},
		"no factory":    {Root: pub, Clock: newFakeClock()},
		"huge skew":     {FactoryID: testFactory, Root: pub, Clock: newFakeClock(), ClockSkew: time.Hour},
		"negative skew": {FactoryID: testFactory, Root: pub, Clock: newFakeClock(), ClockSkew: -time.Second},
	} {
		if _, err := NewChainVerifier(cfg); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: error = %v, want INVALID_ARGUMENT", name, err)
		}
	}
}

func TestDelegatedSignerNeverPrintsItsKey(t *testing.T) {
	t.Parallel()
	c := newChain(t)
	key := c.issue(t, PurposeQAReceipt, HolderSupervisor)
	signer := c.load(t, key)
	pem := string(key.PrivateKey.Reveal())
	body := strings.Join(strings.Split(pem, "\n")[1:2], "")
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		out := fmt.Sprintf(verb, signer) + fmt.Sprintf(verb, *signer) + fmt.Sprintf(verb, key)
		if strings.Contains(out, body) {
			t.Fatalf("%s printed the private key", verb)
		}
	}
}

package signing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The signer and the verifier are what the rest of the factory receives
// through the shared contracts.
var (
	_ core.Signer   = (*Signer)(nil)
	_ core.Verifier = (*Verifier)(nil)
)

// record returns the canonical signing payload of an example record type.
func record(domain, subject string) []byte {
	return core.NewPayloadBuilder(domain, 1).Text("subject", subject).Bytes()
}

func TestApprovalSignedByATrustedKeyVerifies(t *testing.T) {
	t.Parallel()
	signer, pub := newSigner(t)
	approval := approvals.Approval{
		RequestID:      "req-1",
		SubjectDigest:  appDigest,
		PolicyRevision: "0123456789abcdef0123456789abcdef01234567",
		Nonce:          "0123456789abcdef",
		Decision:       approvals.DecisionApprove,
		ActorID:        "owner-1",
		ActorKind:      core.ActorHuman,
		Surface:        core.SurfaceCLI,
		DecidedAt:      time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC),
		ExpiresAt:      time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC),
	}
	sig, err := signer.Sign(context.Background(), approval.SigningPayload())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := sig.Validate(); err != nil {
		t.Fatalf("signature is incomplete: %v", err)
	}
	if signer.KeyID() != KeyID(pub) || sig.KeyID != signer.KeyID() {
		t.Errorf("signature names key %q, signer %q, want %q", sig.KeyID, signer.KeyID(), KeyID(pub))
	}
	verifier := newVerifier(t, pub)
	if err := verifier.Verify(context.Background(), approval.SigningPayload(), sig); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// The same decision moved to another subject is another payload.
	approval.SubjectDigest = otherDigest
	wantCode(t, verifier.Verify(context.Background(), approval.SigningPayload(), sig), core.CodeSignatureInvalid)
}

func TestSignatureIsPlainEd25519OverThePayload(t *testing.T) {
	t.Parallel()
	// A root key kept in a hardware device signs the payload bytes with
	// standard ed25519; such a signature must verify here, and one made here
	// must verify anywhere.
	pub, priv := newKey(t)
	payload := record("sisf.example", "x")
	external := core.Signature{KeyID: KeyID(pub), Algorithm: Algorithm, Value: ed25519.Sign(priv, payload)}
	if err := newVerifier(t, pub).Verify(context.Background(), payload, external); err != nil {
		t.Fatalf("a standard ed25519 signature over the payload was rejected: %v", err)
	}
	signer, err := NewSigner(priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	sig, err := signer.Sign(context.Background(), payload)
	if err != nil || !ed25519.Verify(pub, payload, sig.Value) {
		t.Fatalf("the signer's signature is not standard ed25519 over the payload (%v)", err)
	}
}

func TestSignatureIsRejectedUnlessEverythingMatches(t *testing.T) {
	t.Parallel()
	signer, pub := newSigner(t)
	other, _ := newKey(t)
	payload := record("sisf.example", "release sha256:1111")
	trusted := newVerifier(t, pub)

	cases := map[string]struct {
		verifier *Verifier
		payload  []byte
		change   func(*core.Signature)
	}{
		"payload changed":             {trusted, record("sisf.example", "release sha256:2222"), nil},
		"same fields, another record": {trusted, record("sisf.other", "release sha256:1111"), nil},
		"payload emptied":             {trusted, nil, nil},
		"key not trusted":             {newVerifier(t, other), payload, nil},
		"key id of a trusted key":     {newVerifier(t, other), payload, func(s *core.Signature) { s.KeyID = KeyID(other) }},
		"another algorithm claimed":   {trusted, payload, func(s *core.Signature) { s.Algorithm = "rsa-pss" }},
		"no algorithm":                {trusted, payload, func(s *core.Signature) { s.Algorithm = "" }},
		"no key id":                   {trusted, payload, func(s *core.Signature) { s.KeyID = "" }},
		"no value":                    {trusted, payload, func(s *core.Signature) { s.Value = nil }},
		"value too short":             {trusted, payload, func(s *core.Signature) { s.Value = s.Value[:ed25519.SignatureSize-1] }},
		"value too long":              {trusted, payload, func(s *core.Signature) { s.Value = append(s.Value, 0) }},
		"value altered":               {trusted, payload, func(s *core.Signature) { s.Value[len(s.Value)-1] ^= 0x80 }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sig, err := signer.Sign(context.Background(), payload)
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			if tc.change != nil {
				tc.change(&sig)
			}
			wantCode(t, tc.verifier.Verify(context.Background(), tc.payload, sig), core.CodeSignatureInvalid)
		})
	}
}

func TestOnlyCanonicalSigningPayloadsAreSignedOrVerified(t *testing.T) {
	t.Parallel()
	pub, priv := newKey(t)
	signer, err := NewSigner(priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	verifier := newVerifier(t, pub)
	canonical := record("sisf.example", "x")
	notPayloads := map[string][]byte{
		"empty":                       nil,
		"free text":                   []byte("approve release sha256:1111"),
		"json of a record":            []byte(`{"factory_id":"acme-hello"}`),
		"opening line alone":          canonical[:bytes.IndexByte(canonical, '\n')+1],
		"payload without its opening": canonical[bytes.IndexByte(canonical, '\n')+1:],
		"payload behind other bytes":  append([]byte("x"), canonical...),
	}
	for name, raw := range notPayloads {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := signer.Sign(context.Background(), raw); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("Sign accepted bytes that are not a signing payload: %v", err)
			}
			// Even a correct ed25519 signature over such bytes proves nothing here.
			forged := core.Signature{KeyID: KeyID(pub), Algorithm: Algorithm, Value: ed25519.Sign(priv, raw)}
			wantCode(t, verifier.Verify(context.Background(), raw, forged), core.CodeSignatureInvalid)
		})
	}
}

func TestSignerAndVerifierNeedWellFormedKeys(t *testing.T) {
	t.Parallel()
	pub, priv := newKey(t)
	if _, err := NewSigner(priv[:16]); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a truncated private key was accepted: %v", err)
	}
	if _, err := NewSigner(nil); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a missing private key was accepted: %v", err)
	}
	if _, err := NewVerifier(); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a verifier that trusts no key was built: %v", err)
	}
	if _, err := NewVerifier(pub[:10]); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a truncated public key was trusted: %v", err)
	}
	if _, err := NewVerifier(pub, nil); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a missing public key was trusted: %v", err)
	}
}

func TestSignerKeepsItsOwnCopyOfTheKey(t *testing.T) {
	t.Parallel()
	pub, priv := newKey(t)
	signer, err := NewSigner(priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	clear(priv) // the caller wipes its copy
	payload := record("sisf.example", "x")
	sig, err := signer.Sign(context.Background(), payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := newVerifier(t, pub).Verify(context.Background(), payload, sig); err != nil {
		t.Errorf("signature made after the caller wiped its key does not verify: %v", err)
	}
}

func TestPrintingASignerNeverRevealsThePrivateKey(t *testing.T) {
	t.Parallel()
	_, priv := newKey(t)
	signer, err := NewSigner(priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	seed := priv.Seed()
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%x", "%d"} {
		// The signer is printed both as the pointer callers hold and as a copy.
		for _, out := range []string{fmt.Sprintf(format, signer), fmt.Sprintf(format, *signer)} {
			leaked := strings.Contains(out, fmt.Sprintf("%x", seed)) ||
				strings.Contains(out, strings.Trim(fmt.Sprintf("%d", seed), "[]")) ||
				bytes.Contains([]byte(out), seed)
			if leaked {
				t.Errorf("format %s prints the private key", format)
			}
			if !strings.Contains(out, signer.KeyID()) {
				t.Errorf("format %s = %q, want it to name the key by its id", format, out)
			}
		}
	}
}

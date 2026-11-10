package signing

import (
	"context"
	"crypto/ed25519"
	"slices"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Verifier checks signatures over canonical signing payloads against a fixed
// set of trusted public keys. It implements core.Verifier.
//
// The set does not change after construction. Keys that expire or can be
// revoked are checked by their owner before the key is trusted here.
type Verifier struct {
	keys map[string]ed25519.PublicKey
}

// NewVerifier returns a verifier that trusts exactly the given keys. At
// least one key is required: a verifier that trusts nothing could only be
// built by mistake.
func NewVerifier(trusted ...ed25519.PublicKey) (*Verifier, error) {
	if len(trusted) == 0 {
		return nil, invalid("a verifier needs at least one trusted public key")
	}
	keys := make(map[string]ed25519.PublicKey, len(trusted))
	for _, key := range trusted {
		if len(key) != ed25519.PublicKeySize {
			return nil, invalid("trusted key must be an ed25519 public key of %d bytes", ed25519.PublicKeySize)
		}
		keys[KeyID(key)] = slices.Clone(key)
	}
	return &Verifier{keys: keys}, nil
}

// Verify returns nil only when sig is an ed25519 signature by a trusted key
// over payload, and payload is a canonical signing payload. Every other
// outcome is SIGNATURE_INVALID: an unsigned payload, bytes that are not a
// signing payload, another algorithm, an unknown key, a malformed value or a
// mismatch.
func (v *Verifier) Verify(_ context.Context, payload []byte, sig core.Signature) error {
	if sig.Validate() != nil {
		return rejected("the payload is not signed: the signature lacks a key id, an algorithm or a value")
	}
	if !isRecordPayload(payload) {
		return rejected("only a canonical signing payload can be authenticated")
	}
	if sig.Algorithm != Algorithm {
		return rejected("signature algorithm %q is not accepted; only %s is", sig.Algorithm, Algorithm)
	}
	key, trusted := v.keys[sig.KeyID]
	if !trusted {
		return rejected("signing key %q is not trusted", sig.KeyID)
	}
	if len(sig.Value) != ed25519.SignatureSize || !ed25519.Verify(key, payload, sig.Value) {
		return rejected("the signature does not match the payload")
	}
	return nil
}

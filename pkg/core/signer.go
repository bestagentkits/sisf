package core

import "context"

// Signature is a detached signature over a payload. It names the key and the
// algorithm so a verifier can reject keys that were revoked or have expired.
type Signature struct {
	KeyID     string `json:"key_id"`
	Algorithm string `json:"algorithm"`
	Value     []byte `json:"value"`
}

// IsZero reports whether the signature is entirely unset.
func (s Signature) IsZero() bool { return s.KeyID == "" && s.Algorithm == "" && len(s.Value) == 0 }

// Validate requires a key, an algorithm and a value. It does not verify.
func (s Signature) Validate() error {
	if s.KeyID == "" || s.Algorithm == "" || len(s.Value) == 0 {
		return invalid("signature must carry a key id, an algorithm and a value")
	}
	return nil
}

// Signer signs with one key. Signing keys are held only by the trusted
// release supervisor and the approval service; agents and release candidates
// never receive a Signer.
type Signer interface {
	// KeyID identifies the key that Sign uses.
	KeyID() string
	Sign(ctx context.Context, payload []byte) (Signature, error)
}

// Verifier checks a signature against the keys it trusts. It returns
// SIGNATURE_INVALID when the signature does not match or the key is unknown,
// revoked or expired; only a nil error means the payload is authentic.
type Verifier interface {
	Verify(ctx context.Context, payload []byte, sig Signature) error
}

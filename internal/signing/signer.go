package signing

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"slices"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Signer signs canonical signing payloads with one ed25519 private key. It
// implements core.Signer.
//
// A Signer holds a private key, so it is given only to the trusted release
// supervisor and the approval service. Printing or logging one shows its key
// id and never the key.
type Signer struct {
	key   ed25519.PrivateKey
	keyID string
}

// NewSigner returns a signer for key. It keeps its own copy of key, so the
// caller may wipe the one it passed in.
func NewSigner(key ed25519.PrivateKey) (*Signer, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, invalid("signing key must be an ed25519 private key of %d bytes", ed25519.PrivateKeySize)
	}
	public, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		return nil, invalid("signing key has no ed25519 public key")
	}
	return &Signer{key: slices.Clone(key), keyID: KeyID(public)}, nil
}

// KeyID identifies the key that Sign uses.
func (s *Signer) KeyID() string { return s.keyID }

// Sign returns a detached ed25519 signature over payload. The payload must
// be the SigningPayload of a signed type: bytes that do not open as a
// canonical signing payload are refused, so the key never signs something
// that could later be passed off as another kind of record.
func (s *Signer) Sign(_ context.Context, payload []byte) (core.Signature, error) {
	if !isRecordPayload(payload) {
		return core.Signature{}, invalid("refusing to sign bytes that are not a canonical signing payload")
	}
	return core.Signature{KeyID: s.keyID, Algorithm: Algorithm, Value: ed25519.Sign(s.key, payload)}, nil
}

// Format prints the signer as its key id for every formatting
// verb, so a signer that reaches a log line or an error never reveals its
// private key.
func (s Signer) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprintf(state, "signing.Signer(%s)", s.keyID) // the state reports no error worth acting on
}

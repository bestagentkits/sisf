package delegation

import (
	"context"
	"slices"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Domain and field-list version of the revocation list's signing payload.
const (
	revocationPayloadDomain  = "sisf.key_revocations"
	revocationPayloadVersion = 1
)

// RevocationList is the root key's list of revoked delegated keys. Each list
// carries a sequence number that only grows, so an older list cannot be
// replayed to bring a revoked key back. A verifier keeps every key it has
// seen revoked, whatever a later list says.
type RevocationList struct {
	FactoryID core.FactoryID `json:"factory_id"`
	// RootKeyID names the root key that signs the list.
	RootKeyID string `json:"root_key_id"`
	// Sequence is at least 1 and higher than that of every earlier list.
	Sequence int64     `json:"sequence"`
	IssuedAt time.Time `json:"issued_at"`
	// KeyIDs are the revoked delegated keys.
	KeyIDs []string `json:"key_ids"`
}

// Validate rejects a list that is incomplete or names a key twice. It does
// not verify a signature.
func (l RevocationList) Validate() error {
	if err := l.FactoryID.Validate(); err != nil {
		return err
	}
	switch {
	case l.RootKeyID == "":
		return invalid("revocation list must name the root key that signs it")
	case l.Sequence < 1:
		return invalid("revocation list sequence must be at least 1")
	case l.IssuedAt.IsZero():
		return invalid("revocation list must say when it was issued")
	}
	seen := map[string]bool{}
	for _, id := range l.KeyIDs {
		if id == "" || seen[id] {
			return invalid("revocation list names an empty or repeated key id")
		}
		seen[id] = true
	}
	return nil
}

// SigningPayload returns the canonical bytes the root key signs. The key ids
// are counted and sorted, so the same set always yields the same bytes and
// no id can be added or dropped without invalidating the signature.
func (l RevocationList) SigningPayload() []byte {
	b := core.NewPayloadBuilder(revocationPayloadDomain, revocationPayloadVersion).
		Text("factory_id", string(l.FactoryID)).
		Text("root_key_id", l.RootKeyID).
		Int("sequence", l.Sequence).
		Time("issued_at", l.IssuedAt).
		Int("keys", int64(len(l.KeyIDs)))
	for _, id := range slices.Sorted(slices.Values(l.KeyIDs)) {
		b.Text("key_id", id)
	}
	return b.Bytes()
}

// SignedRevocationList is a revocation list with the root key's signature.
type SignedRevocationList struct {
	List      RevocationList `json:"list"`
	Signature core.Signature `json:"signature,omitzero"`
}

// SignRevocations signs l with root, the Owner's root key. The list's
// RootKeyID is set from the signer. An invalid list is never signed.
func SignRevocations(ctx context.Context, root core.Signer, l RevocationList) (SignedRevocationList, error) {
	if root == nil {
		return SignedRevocationList{}, invalid("signing a revocation list needs the root signer")
	}
	l.RootKeyID = root.KeyID()
	l.IssuedAt = l.IssuedAt.UTC()
	l.KeyIDs = slices.Sorted(slices.Values(l.KeyIDs))
	if err := l.Validate(); err != nil {
		return SignedRevocationList{}, err
	}
	sig, err := root.Sign(ctx, l.SigningPayload())
	if err != nil {
		return SignedRevocationList{}, err
	}
	return SignedRevocationList{List: l, Signature: sig}, nil
}

package delegation

import (
	"context"
	"fmt"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
)

// LoadRequest is what a trusted service presents to load its delegated key.
type LoadRequest struct {
	// Holder is the service loading the key. Only the supervisor and the
	// approval service may, and only a key issued to them.
	Holder     Holder
	Delegation SignedDelegation
	// PrivateKey is IssuedKey.PrivateKey, read from the holder's Zone 3 store.
	PrivateKey core.SecretValue
}

// DelegatedSigner signs with a delegated key. It implements core.Signer and
// refuses to sign a record of another kind than its purpose, or once its key
// is revoked or outside its window.
type DelegatedSigner struct {
	signer   *signing.Signer
	verifier *ChainVerifier
	purpose  Purpose
	holder   Holder
}

// Load checks a delegated key and returns its signer. The delegation must be
// issued to req.Holder, which must be the supervisor or the approval
// service; it is added to verifier, which checks the root signature; the key
// must be usable now; and the private key must be the delegated one.
func Load(ctx context.Context, verifier *ChainVerifier, req LoadRequest) (*DelegatedSigner, error) {
	if verifier == nil {
		return nil, invalid("loading a delegated key needs the chain verifier")
	}
	d := req.Delegation.Delegation
	// A trusted delegation names only the supervisor or the approval service
	// as holder, so matching it admits no one else.
	if req.Holder != d.Holder {
		return nil, denied("the delegated key was issued to %s, not %q", d.Holder, req.Holder)
	}
	if err := verifier.Trust(ctx, req.Delegation); err != nil {
		return nil, err
	}
	if _, reason := verifier.usable(d.KeyID, opening(d.Purpose)); reason != "" {
		return nil, denied("%s", reason)
	}
	pem := req.PrivateKey.Reveal()
	priv, err := signing.ParsePrivateKey(pem)
	clear(pem)
	if err != nil {
		return nil, err
	}
	defer clear(priv)
	signer, err := signing.NewSigner(priv)
	if err != nil {
		return nil, err
	}
	if signer.KeyID() != d.KeyID {
		return nil, invalid("the private key is not the delegated key")
	}
	return &DelegatedSigner{signer: signer, verifier: verifier, purpose: d.Purpose, holder: d.Holder}, nil
}

// opening is the smallest payload of a purpose's record type, used to ask
// whether a key is usable for that purpose at all.
func opening(p Purpose) []byte { return domainPrefix(purposeDomains[p]) }

// KeyID identifies the delegated key.
func (s *DelegatedSigner) KeyID() string { return s.signer.KeyID() }

// Purpose is the kind of record the key signs.
func (s *DelegatedSigner) Purpose() Purpose { return s.purpose }

// Sign signs payload when it is a record of the key's purpose and the key is
// still trusted, unrevoked and within its window; otherwise it returns
// PERMISSION_DENIED and signs nothing.
func (s *DelegatedSigner) Sign(ctx context.Context, payload []byte) (core.Signature, error) {
	if _, reason := s.verifier.usable(s.signer.KeyID(), payload); reason != "" {
		return core.Signature{}, denied("%s", reason)
	}
	return s.signer.Sign(ctx, payload)
}

// Format prints the signer as its holder, purpose and key id, never the key.
func (s DelegatedSigner) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprintf(state, "delegation.DelegatedSigner(%s %s %s)", s.holder, s.purpose, s.signer.KeyID()) // the state reports no error worth acting on
}

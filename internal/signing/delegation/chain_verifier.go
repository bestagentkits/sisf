package delegation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"slices"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
)

// DefaultClockSkew is how far ahead of the factory's clock a delegation may
// start and still be trusted, for an Owner whose clock runs slightly fast.
const DefaultClockSkew = 2 * time.Minute

// maxClockSkew bounds the configured tolerance.
const maxClockSkew = 10 * time.Minute

// Assurance is how well the root key is protected from the factory itself.
type Assurance string

const (
	// AssuranceRootOffline means the Owner holds the root key outside the
	// factory (KMS, HSM or a hardware key).
	AssuranceRootOffline Assurance = "root_offline"
	// AssuranceLower is the local native profile: the root key sits in the
	// factory's Zone 3 secret store, so whoever reads that store can issue
	// keys. Every surface must show it.
	AssuranceLower Assurance = "lower_root_in_factory_store"
)

// Lower reports whether the assurance is the lower, local-profile level.
func (a Assurance) Lower() bool { return a != AssuranceRootOffline }

// VerifierConfig describes the trust anchor of a ChainVerifier.
type VerifierConfig struct {
	// FactoryID is the one deployment whose delegations are trusted.
	FactoryID core.FactoryID
	// Root is the Owner's root public key.
	Root ed25519.PublicKey
	// Clock decides whether a delegation is within its window. Required.
	Clock core.Clock
	// ClockSkew is how early a delegation may be used before its start.
	// Zero means DefaultClockSkew; it never extends the end of a window.
	ClockSkew time.Duration
	// RootKeyInFactoryStore marks the local native profile, where the root
	// key is kept in the Zone 3 secret store; Assurance reports it.
	RootKeyInFactoryStore bool
}

// ChainVerifier verifies signatures by delegated keys. It implements
// core.Verifier: a signature is good only when its key has a delegation
// signed by the root key for this factory, the delegation is within its
// window, the key is not revoked, and the payload is a record of the kind
// the key's purpose allows. It is safe for concurrent use.
type ChainVerifier struct {
	factory   core.FactoryID
	rootID    string
	root      *signing.Verifier
	clock     core.Clock
	skew      time.Duration
	assurance Assurance

	mu          sync.RWMutex
	keys        map[string]trustedKey
	revoked     map[string]bool
	revocations SignedRevocationList
}

// trustedKey is a delegation whose root signature has been checked.
type trustedKey struct {
	delegation Delegation
	verifier   *signing.Verifier
}

// NewChainVerifier returns a verifier that trusts no delegated key yet.
func NewChainVerifier(cfg VerifierConfig) (*ChainVerifier, error) {
	if err := cfg.FactoryID.Validate(); err != nil {
		return nil, err
	}
	if cfg.Clock == nil {
		return nil, invalid("a chain verifier needs a clock")
	}
	if cfg.ClockSkew < 0 || cfg.ClockSkew > maxClockSkew {
		return nil, invalid("clock skew must be within 0-%s", maxClockSkew)
	}
	if cfg.ClockSkew == 0 {
		cfg.ClockSkew = DefaultClockSkew
	}
	root, err := signing.NewVerifier(cfg.Root)
	if err != nil {
		return nil, err
	}
	assurance := AssuranceRootOffline
	if cfg.RootKeyInFactoryStore {
		assurance = AssuranceLower
	}
	return &ChainVerifier{
		factory: cfg.FactoryID, rootID: signing.KeyID(cfg.Root), root: root, clock: cfg.Clock,
		skew: cfg.ClockSkew, assurance: assurance,
		keys: map[string]trustedKey{}, revoked: map[string]bool{},
	}, nil
}

// Assurance reports how well the root key behind this verifier is kept.
func (v *ChainVerifier) Assurance() Assurance { return v.assurance }

// Trust adds a delegated key after checking its delegation: signed by this
// verifier's root key, for this factory, well formed. A delegation that
// fails any check is SIGNATURE_INVALID or INVALID_ARGUMENT and is not added.
// A revoked key is refused. Trusting a key does not make it usable outside
// its window; Verify checks the window at every call.
func (v *ChainVerifier) Trust(ctx context.Context, sd SignedDelegation) error {
	d := sd.Delegation
	if err := v.checkRootSigned(ctx, d.FactoryID, d.RootKeyID, d.SigningPayload(), sd.Signature); err != nil {
		return err
	}
	if err := d.Validate(); err != nil {
		return err
	}
	pub, err := d.publicKey()
	if err != nil {
		return err
	}
	keyVerifier, err := signing.NewVerifier(pub)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.revoked[d.KeyID] {
		return rejected("delegated key %s is revoked", d.KeyID)
	}
	if existing, ok := v.keys[d.KeyID]; ok && !bytes.Equal(existing.delegation.SigningPayload(), d.SigningPayload()) {
		return invalid("delegated key %s is already trusted under another delegation", d.KeyID)
	}
	v.keys[d.KeyID] = trustedKey{delegation: d, verifier: keyVerifier}
	return nil
}

// ApplyRevocations takes a root-signed revocation list. Its sequence must be
// higher than that of the last list applied; the same list applied again is
// accepted and changes nothing, and an older one is REPLAYED. Keys revoked
// by an earlier list stay revoked.
func (v *ChainVerifier) ApplyRevocations(ctx context.Context, sl SignedRevocationList) error {
	l := sl.List
	if err := v.checkRootSigned(ctx, l.FactoryID, l.RootKeyID, l.SigningPayload(), sl.Signature); err != nil {
		return err
	}
	if err := l.Validate(); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	current := v.revocations.List.Sequence
	if l.Sequence <= current {
		if l.Sequence == current && bytes.Equal(l.SigningPayload(), v.revocations.List.SigningPayload()) {
			return nil
		}
		return core.NewError(core.CodeReplayed, "the revocation list is not newer than the one in force")
	}
	for _, id := range l.KeyIDs {
		v.revoked[id] = true
	}
	sl.List.KeyIDs = slices.Clone(l.KeyIDs)
	v.revocations = sl
	return nil
}

// Revoked reports whether a key id has been revoked.
func (v *ChainVerifier) Revoked(keyID string) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.revoked[keyID]
}

// Verify returns nil only when sig is a good signature over payload by a
// trusted delegated key that is usable now for the payload's kind of
// record. Every other outcome is SIGNATURE_INVALID.
func (v *ChainVerifier) Verify(ctx context.Context, payload []byte, sig core.Signature) error {
	if sig.Validate() != nil {
		return rejected("the payload is not signed")
	}
	key, reason := v.usable(sig.KeyID, payload)
	if reason != "" {
		return rejected("%s", reason)
	}
	return key.verifier.Verify(ctx, payload, sig)
}

// usable returns the trusted key when it may sign or verify payload now, and
// otherwise the reason it may not.
func (v *ChainVerifier) usable(keyID string, payload []byte) (trustedKey, string) {
	now := v.clock.Now()
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.revoked[keyID] {
		return trustedKey{}, "delegated key " + keyID + " is revoked"
	}
	key, ok := v.keys[keyID]
	if !ok {
		return trustedKey{}, "key " + keyID + " has no trusted delegation"
	}
	d := key.delegation
	switch {
	case now.Before(d.NotBefore.Add(-v.skew)):
		return trustedKey{}, "delegated key " + keyID + " is not valid yet"
	case !now.Before(d.NotAfter):
		return trustedKey{}, "delegated key " + keyID + " has expired"
	case !d.Purpose.Covers(payload):
		return trustedKey{}, "delegated key " + keyID + " may sign only " + string(d.Purpose) + " records"
	}
	return key, ""
}

// checkRootSigned verifies a root signature over a statement that names its
// factory and root key.
func (v *ChainVerifier) checkRootSigned(ctx context.Context, factory core.FactoryID, rootKeyID string, payload []byte, sig core.Signature) error {
	if sig.Validate() != nil {
		return rejected("the statement is not signed by the root key")
	}
	if sig.KeyID != v.rootID || rootKeyID != v.rootID {
		return rejected("the statement is not signed by this factory's root key")
	}
	if err := v.root.Verify(ctx, payload, sig); err != nil {
		return err
	}
	if factory != v.factory {
		return rejected("the statement was issued for another factory")
	}
	return nil
}

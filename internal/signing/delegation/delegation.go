// Package delegation issues, verifies and revokes the short-lived signing
// keys a factory holds on behalf of the Owner's root key.
//
// The root key stays with the Owner, outside the factory. It signs a
// delegation for each factory key: the key's public half, the one purpose it
// may sign for, the service that may hold it and the window in which it is
// valid. A ChainVerifier trusts a delegated key only through such a
// delegation, checked against the root public key, and refuses a key that is
// revoked, outside its window or used for a record of another kind. The
// Owner revokes a delegated key with a root-signed revocation list, so a
// compromised Zone 3 store is recovered from without replacing the root.
//
// Only the trusted release supervisor and the approval service load a
// delegated key; agents and candidates never receive one.
package delegation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
)

// MaxLifetime bounds how long one delegated key is valid. A delegation for a
// longer window is never issued and never trusted, so a leaked key stops
// working on its own within this time even if nobody revokes it.
const MaxLifetime = 7 * 24 * time.Hour

// Domain and field-list version of the delegation's signing payload.
const (
	delegationPayloadDomain  = "sisf.key_delegation"
	delegationPayloadVersion = 1
)

// Purpose is the one kind of record a delegated key may sign.
type Purpose string

const (
	// PurposeQAReceipt signs core.QAReceipt payloads.
	PurposeQAReceipt Purpose = "qa_receipt"
	// PurposeApproval signs approvals.Approval payloads.
	PurposeApproval Purpose = "approval"
	// PurposeDeployArtifact signs deploy.Artifact payloads.
	PurposeDeployArtifact Purpose = "deploy_artifact"
)

// purposeDomains maps each purpose to the payload domain of the record type
// it signs. The domains are those the record types write with
// core.NewPayloadBuilder; a test checks them against the real payloads.
var purposeDomains = map[Purpose]string{
	PurposeQAReceipt:      "sisf.qa_receipt",
	PurposeApproval:       "sisf.approval",
	PurposeDeployArtifact: "sisf.deploy_artifact",
}

// Valid reports whether p is a known purpose.
func (p Purpose) Valid() bool { _, ok := purposeDomains[p]; return ok }

// Covers reports whether payload is a signing payload of the record type p
// signs. The domain is length-prefixed in the payload, so one domain can
// never pass for another that shares its first letters.
func (p Purpose) Covers(payload []byte) bool {
	domain, ok := purposeDomains[p]
	return ok && bytes.HasPrefix(payload, domainPrefix(domain))
}

// domainPrefix returns the opening of every payload of one domain: the fixed
// first line and the domain line, without the version that follows.
func domainPrefix(domain string) []byte {
	opening := core.NewPayloadBuilder(domain, 0).Bytes()
	withoutVersion := bytes.LastIndexByte(opening[:len(opening)-1], '\n')
	return opening[:withoutVersion+1]
}

// Holder is the trusted service a delegated key is issued to.
type Holder string

const (
	// HolderSupervisor is the trusted release supervisor. It signs QA
	// receipts as the trusted evaluator and deploy artifacts.
	HolderSupervisor Holder = "supervisor"
	// HolderApprovalService is the approval service. It signs approvals.
	HolderApprovalService Holder = "approval_service"
)

// holderPurposes lists what each holder may sign. A key for any other pair
// is never issued, trusted or loaded.
var holderPurposes = map[Holder][]Purpose{
	HolderSupervisor:      {PurposeQAReceipt, PurposeDeployArtifact},
	HolderApprovalService: {PurposeApproval},
}

// Valid reports whether h is one of the services that may hold a key.
func (h Holder) Valid() bool { _, ok := holderPurposes[h]; return ok }

// MayHold reports whether h may hold a key for purpose p.
func (h Holder) MayHold(p Purpose) bool { return slices.Contains(holderPurposes[h], p) }

// Delegation is the root key's statement that one public key may sign one
// kind of record for one factory, held by one service, within a window.
type Delegation struct {
	FactoryID core.FactoryID `json:"factory_id"`
	// RootKeyID names the root key that signs the delegation.
	RootKeyID string `json:"root_key_id"`
	// KeyID is signing.KeyID of PublicKey.
	KeyID string `json:"key_id"`
	// PublicKey is the delegated public key, encoded by signing.EncodePublicKey.
	PublicKey string    `json:"public_key"`
	Purpose   Purpose   `json:"purpose"`
	Holder    Holder    `json:"holder"`
	NotBefore time.Time `json:"not_before"`
	// NotAfter is the first instant at which the key is no longer valid.
	NotAfter time.Time `json:"not_after"`
}

// Validate rejects a delegation that is incomplete, names a key that does
// not match its id, pairs a holder with a purpose it may not sign, or is
// valid for longer than MaxLifetime. It does not verify a signature.
func (d Delegation) Validate() error {
	if err := d.FactoryID.Validate(); err != nil {
		return err
	}
	if d.RootKeyID == "" {
		return invalid("delegation must name the root key that signs it")
	}
	if !d.Purpose.Valid() || !d.Holder.Valid() {
		return invalid("delegation has an unknown purpose or holder")
	}
	if !d.Holder.MayHold(d.Purpose) {
		return invalid("holder %s may not hold a key for %s", d.Holder, d.Purpose)
	}
	pub, err := d.publicKey()
	if err != nil {
		return err
	}
	if signing.KeyID(pub) != d.KeyID {
		return invalid("delegation key id does not match its public key")
	}
	switch {
	case d.NotBefore.IsZero() || !d.NotAfter.After(d.NotBefore):
		return invalid("delegation needs a window whose end follows its start")
	case d.NotAfter.Sub(d.NotBefore) > MaxLifetime:
		return invalid("delegation is valid for longer than %s", MaxLifetime)
	}
	return nil
}

// publicKey decodes the delegated public key.
func (d Delegation) publicKey() (ed25519.PublicKey, error) {
	return signing.ParsePublicKey(d.PublicKey)
}

// SigningPayload returns the canonical bytes the root key signs. Every field
// is bound in a fixed order, so no field can be changed, and the delegation
// cannot be moved to another factory, without invalidating its signature.
func (d Delegation) SigningPayload() []byte {
	return core.NewPayloadBuilder(delegationPayloadDomain, delegationPayloadVersion).
		Text("factory_id", string(d.FactoryID)).
		Text("root_key_id", d.RootKeyID).
		Text("key_id", d.KeyID).
		Text("public_key", d.PublicKey).
		Text("purpose", string(d.Purpose)).
		Text("holder", string(d.Holder)).
		Time("not_before", d.NotBefore).
		Time("not_after", d.NotAfter).
		Bytes()
}

// SignedDelegation is a delegation with the root key's signature.
type SignedDelegation struct {
	Delegation Delegation     `json:"delegation"`
	Signature  core.Signature `json:"signature,omitzero"`
}

// IssueRequest asks for a new delegated key.
type IssueRequest struct {
	FactoryID core.FactoryID
	Purpose   Purpose
	Holder    Holder
	NotBefore time.Time
	// Lifetime is how long the key is valid from NotBefore, at most MaxLifetime.
	Lifetime time.Duration
}

// IssuedKey is a new delegated key: the signed delegation, which is public,
// and the private key, which belongs in the Zone 3 secret store of its
// holder and nowhere else.
type IssuedKey struct {
	Delegation SignedDelegation
	// PrivateKey is the PKCS #8 PEM encoding of the delegated private key.
	PrivateKey core.SecretValue
}

// Issue generates a delegated key pair and signs its delegation with root,
// the Owner's root key. random supplies the key material; nil means
// crypto/rand. Issue runs where the root key is, with the Owner, not in the
// factory.
func Issue(ctx context.Context, root core.Signer, req IssueRequest, random io.Reader) (IssuedKey, error) {
	if root == nil {
		return IssuedKey{}, invalid("issuing a delegated key needs the root signer")
	}
	if random == nil {
		random = rand.Reader
	}
	if req.Lifetime <= 0 {
		return IssuedKey{}, invalid("delegated key needs a positive lifetime")
	}
	pub, priv, err := ed25519.GenerateKey(random)
	if err != nil {
		return IssuedKey{}, core.NewError(core.CodeUnavailable, "no random bytes are available to make a delegated key").WithCause(err)
	}
	d := Delegation{
		FactoryID: req.FactoryID,
		RootKeyID: root.KeyID(),
		KeyID:     signing.KeyID(pub),
		PublicKey: signing.EncodePublicKey(pub),
		Purpose:   req.Purpose,
		Holder:    req.Holder,
		NotBefore: req.NotBefore.UTC(),
		NotAfter:  req.NotBefore.Add(req.Lifetime).UTC(),
	}
	if err := d.Validate(); err != nil {
		return IssuedKey{}, err
	}
	sig, err := root.Sign(ctx, d.SigningPayload())
	if err != nil {
		return IssuedKey{}, err
	}
	encoded, err := signing.EncodePrivateKey(priv)
	if err != nil {
		return IssuedKey{}, err
	}
	key := IssuedKey{Delegation: SignedDelegation{Delegation: d, Signature: sig}, PrivateKey: core.NewSecretValue(encoded)}
	clear(encoded)
	clear(priv)
	return key, nil
}

// invalid builds the INVALID_ARGUMENT error used by this package.
func invalid(format string, args ...any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, fmt.Sprintf(format, args...))
}

// rejected builds the SIGNATURE_INVALID error: only a nil error from a
// verifier means the payload is authentic.
func rejected(format string, args ...any) *core.Error {
	return core.NewError(core.CodeSignatureInvalid, fmt.Sprintf(format, args...))
}

// denied builds the PERMISSION_DENIED error.
func denied(format string, args ...any) *core.Error {
	return core.NewError(core.CodePermissionDenied, fmt.Sprintf(format, args...))
}

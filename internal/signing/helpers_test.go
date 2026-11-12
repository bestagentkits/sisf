package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Digests used by the fixtures. They only need to be well formed.
const (
	appDigest        core.Digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	supervisorDigest core.Digest = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	executableDigest core.Digest = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
	otherDigest      core.Digest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

// newKey generates a key pair for one test. No key is ever read from the
// repository.
func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

// newSigner returns a signer with a fresh key, and its public key.
func newSigner(t *testing.T) (*Signer, ed25519.PublicKey) {
	t.Helper()
	pub, priv := newKey(t)
	signer, err := NewSigner(priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return signer, pub
}

// newVerifier returns a verifier that trusts keys.
func newVerifier(t *testing.T, keys ...ed25519.PublicKey) *Verifier {
	t.Helper()
	verifier, err := NewVerifier(keys...)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return verifier
}

// validManifest is a release manifest that passes validation.
func validManifest() ReleaseManifest {
	return ReleaseManifest{
		APIVersion: ReleaseAPIVersion,
		FactoryID:  "acme-hello",
		IssuedAt:   time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC),
		Units: map[manifest.UnitName]core.Digest{
			manifest.UnitApp:        appDigest,
			manifest.UnitSupervisor: supervisorDigest,
		},
		SupervisorExecutable: executableDigest,
	}
}

// wantCode fails the test unless err carries code.
func wantCode(t *testing.T, err error, code core.ErrorCode) {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

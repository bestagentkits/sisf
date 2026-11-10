package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// SignatureHeader carries the HMAC SHA-256 of the raw request body.
const SignatureHeader = "X-Hub-Signature-256"

const signaturePrefix = "sha256="

// Sign returns the header value GitHub would send for body under secret.
// The fake GitHub and tests use it; the receiver uses the same function so
// both sides agree on the format.
func Sign(secret core.SecretValue, body []byte) string {
	mac := hmac.New(sha256.New, secret.Reveal())
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// verifySignature accepts the delivery when header is the HMAC of body under
// any one of the active secrets, which lets the secret rotate without losing
// events. Every secret is compared and the comparison is constant time.
//
// An empty secret is skipped: an HMAC under an empty key can be forged by
// anyone, so it must never count as configured.
func verifySignature(header string, body []byte, secrets []core.SecretValue) error {
	if header == "" {
		return core.NewError(core.CodeUnauthenticated, "the delivery carries no signature")
	}
	hexPart, ok := strings.CutPrefix(header, signaturePrefix)
	if !ok {
		return core.NewError(core.CodeSignatureInvalid, "the delivery signature is not a sha256 signature")
	}
	got, err := hex.DecodeString(hexPart)
	if err != nil || len(got) != sha256.Size {
		return core.NewError(core.CodeSignatureInvalid, "the delivery signature is malformed")
	}
	active, matched := 0, false
	for _, secret := range secrets {
		if secret.IsZero() {
			continue
		}
		active++
		mac := hmac.New(sha256.New, secret.Reveal())
		mac.Write(body)
		if hmac.Equal(got, mac.Sum(nil)) {
			matched = true
		}
	}
	if active == 0 {
		return core.NewError(core.CodeUnauthenticated, "no webhook secret is configured, so no delivery can be verified")
	}
	if !matched {
		return core.NewError(core.CodeSignatureInvalid, "the delivery signature does not match any active secret")
	}
	return nil
}

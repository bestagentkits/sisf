package signing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"strings"
)

// Algorithm is the only signature algorithm this package produces or accepts.
const Algorithm = "ed25519"

// PEM block types of the two key encodings.
const (
	pemPublicKey  = "PUBLIC KEY"
	pemPrivateKey = "PRIVATE KEY"
)

// KeyID returns the identifier of a public key: "ed25519:" followed by the
// SHA-256 of the key in hex. A signature names its key by this value; the
// verifier looks the key up and still checks the signature against the key
// itself, so claiming another key's id gains nothing.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return Algorithm + ":" + hex.EncodeToString(sum[:])
}

// EncodePublicKey renders a public key as one line of base64, a form that
// fits an environment variable.
func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// ParsePublicKey reads a public key written either as one line of base64
// (the 32 raw bytes) or as a PEM "PUBLIC KEY" block. The error never repeats
// the text, in case a private key was supplied by mistake.
func ParsePublicKey(text string) (ed25519.PublicKey, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "-----BEGIN") {
		return parsePublicPEM([]byte(text))
	}
	raw, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, invalid("public key must be %d bytes in base64 or a PEM %q block", ed25519.PublicKeySize, pemPublicKey)
	}
	return ed25519.PublicKey(raw), nil
}

// parsePublicPEM reads a PKIX-encoded ed25519 public key.
func parsePublicPEM(data []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != pemPublicKey {
		return nil, invalid("expected a PEM %q block holding an ed25519 public key", pemPublicKey)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, invalid("the PEM %q block does not hold a readable public key", pemPublicKey)
	}
	pub, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, invalid("the public key is not an ed25519 key")
	}
	return pub, nil
}

// EncodePrivateKey renders a private key as a PKCS #8 PEM block. The result
// is a secret: it belongs in the Owner's keeping or a secret store, never in
// the repository or a log.
func EncodePrivateKey(priv ed25519.PrivateKey) ([]byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, invalid("private key must be an ed25519 key of %d bytes", ed25519.PrivateKeySize)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, invalid("the private key cannot be encoded").WithCause(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemPrivateKey, Bytes: der}), nil
}

// ParsePrivateKey reads a private key written by EncodePrivateKey. The error
// never repeats the input.
func ParsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != pemPrivateKey {
		return nil, invalid("expected a PEM %q block holding an ed25519 private key", pemPrivateKey)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, invalid("the PEM %q block does not hold a readable private key", pemPrivateKey)
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, invalid("the private key is not an ed25519 key")
	}
	return priv, nil
}

package fakeoidc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/bestagentkits/sisf/pkg/core"
)

// processKeys generates the signing key and the unpublished "rogue" key once
// per process. RSA key generation is slow, and every fake in a test binary
// can share keys because each has its own issuer; a new process, such as
// each run of the CLI, gets new keys.
var processKeys = sync.OnceValues(func() ([2]*rsa.PrivateKey, error) {
	var keys [2]*rsa.PrivateKey
	for i := range keys {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return keys, core.NewError(core.CodeInternal, "the fake provider could not generate a key").WithCause(err)
		}
		keys[i] = k
	}
	return keys, nil
})

func newKeys() (signing, rogue *rsa.PrivateKey, err error) {
	keys, err := processKeys()
	return keys[0], keys[1], err
}

// keyID derives a stable key ID from the public key.
func keyID(pub *rsa.PublicKey) string {
	sum := sha256.Sum256(x509.MarshalPKCS1PublicKey(pub))
	return base64.RawURLEncoding.EncodeToString(sum[:8])
}

// jwks returns the published key set: the signing key only.
func (s *Server) jwks() jose.JSONWebKeySet {
	return jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: &s.signing.PublicKey, KeyID: s.keyID, Algorithm: string(jose.RS256), Use: "sig",
	}}}
}

// idTokenClaims is the Google-style ID token payload.
type idTokenClaims struct {
	jwt.Claims
	Nonce         string `json:"nonce,omitempty"`
	AuthTime      int64  `json:"auth_time,omitempty"`
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified,omitempty"`
	Name          string `json:"name,omitempty"`
}

// signIDToken issues an ID token for g with the active faults applied.
// Callers hold s.mu.
func (s *Server) signIDToken(g grant, u User, now time.Time) (string, error) {
	f := s.faults
	lifetime := DefaultTokenLifetime
	if f.Lifetime != 0 {
		lifetime = f.Lifetime
	}
	claims := idTokenClaims{
		Claims: jwt.Claims{
			Issuer:   pick(f.Issuer, s.issuer),
			Subject:  u.Subject,
			Audience: jwt.Audience{pick(f.Audience, g.clientID)},
			IssuedAt: jwt.NewNumericDate(now),
			Expiry:   jwt.NewNumericDate(now.Add(lifetime)),
		},
		Nonce: pick(f.Nonce, g.nonce), Email: u.Email, EmailVerified: u.EmailVerified, Name: u.Name,
	}
	if at, ok := s.authAt[u.Subject]; ok {
		claims.AuthTime = at.Unix()
	}
	key, kid := s.signing, s.keyID
	if f.UnknownKey {
		key, kid = s.rogue, keyID(&s.rogue.PublicKey)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: kid}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	sig, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return sig.CompactSerialize()
}

// pick returns override when it is set, otherwise value.
func pick(override, value string) string {
	if override != "" {
		return override
	}
	return value
}

// randomString returns a URL-safe random string of n random bytes.
func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

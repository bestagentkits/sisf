// Package auth establishes who is calling the factory.
//
// People sign in through a login provider: GitHub OAuth or Google-style
// OpenID Connect. A login identity is keyed by issuer and subject and never by
// email. Every ID token is checked for signature, issuer, audience, expiry
// and nonce, and any failed check is UNAUTHENTICATED with no partial result.
//
// A browser then holds an HttpOnly, SameSite session cookie; state-changing
// requests made with that cookie also carry the session's CSRF token. The CLI
// and API use short-lived, scoped personal access tokens, and agents and
// services use short-lived service tokens. A bearer token always wins over a
// cookie, so a caller holding a token never borrows an ambient session.
//
// Step-up sends a signed-in person through a fresh sign-in at their provider
// and records when it happened; sensitive actions demand that it is recent.
// Sessions and tokens can be revoked singly or for a whole actor, and a
// revoked one stops working at once.
//
// This package authenticates only. What an actor may do is decided at action
// time by the authorizer, from GitHub membership and local grants; login
// grants no repository permission.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"github.com/bestagentkits/sisf/pkg/core"
)

// randomToken returns n random bytes encoded URL-safe.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", core.NewError(core.CodeInternal, "no randomness available").WithCause(err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// digest is the lookup key a secret is stored under, so the store never
// holds a usable session ID or token.
func digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func unauthenticated(message string) *core.Error {
	return core.NewError(core.CodeUnauthenticated, message)
}

func invalidArgument(message string) *core.Error {
	return core.NewError(core.CodeInvalidArgument, message)
}

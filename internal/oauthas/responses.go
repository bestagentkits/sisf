package oauthas

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

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

// digest is the key a secret is stored under, so memory never holds a usable
// code or consent ID as a map key.
func digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// messageOf returns the caller-safe message of a typed error.
func messageOf(err error) string {
	var typed *core.Error
	if errors.As(err, &typed) {
		return typed.Message
	}
	return "the request failed"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeOAuthError answers with an RFC 6749 error object.
func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

// redirectError sends the person back to a verified redirect URI with an
// error, the client's state and this server's issuer (RFC 9207).
func (s *Server) redirectError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, description string) {
	q := url.Values{"error": {code}, "error_description": {description}, "iss": {s.cfg.Issuer}}
	if state != "" {
		q.Set("state", state)
	}
	http.Redirect(w, r, withQuery(redirectURI, q), http.StatusFound)
}

// withQuery appends q to the query of a redirect URI, keeping any query the
// registered URI already has.
func withQuery(uri string, q url.Values) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	merged := u.Query()
	for k, vs := range q {
		merged[k] = vs
	}
	u.RawQuery = merged.Encode()
	return u.String()
}

// pageError answers a request whose redirect URI could not be trusted, so
// the error is shown here instead of being sent anywhere.
func pageError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message + "\n"))
}

package oauthas

import (
	"errors"
	"mime"
	"net/http"
	"strings"
)

// tokenParams may each appear once at most.
var tokenParams = []string{"grant_type", "code", "redirect_uri", "client_id", "code_verifier", "resource"}

// handleToken exchanges an authorization code for an access token. Only
// public clients exist, so any client authentication is refused, and
// parameters are read from the form body only: a URL never carries them.
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "token request parameters belong in the form body, never in the URL")
		return
	}
	if ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || ct != "application/x-www-form-urlencoded" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "the body must be application/x-www-form-urlencoded")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "the form body is malformed")
		return
	}
	f := r.PostForm
	for _, p := range tokenParams {
		if len(f[p]) > 1 {
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", "parameter "+p+" appears more than once")
			return
		}
	}
	switch {
	case r.Header.Get("Authorization") != "" || f.Has("client_secret") || f.Has("client_assertion"):
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "clients here are public; send client_id and the PKCE verifier, no secret")
		return
	case f.Get("grant_type") != "authorization_code":
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "only the authorization_code grant is supported")
		return
	}
	if _, ok := s.client(f.Get("client_id")); !ok {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "the client is not registered")
		return
	}
	g, err := s.redeemCode(f.Get("code"), redemption{clientID: f.Get("client_id"), redirectURI: f.Get("redirect_uri"),
		verifier: f.Get("code_verifier"), resource: f.Get("resource")})
	var mismatch *bindingFailure
	switch {
	case errors.As(err, &mismatch):
		writeOAuthError(w, http.StatusBadRequest, bindingError(mismatch.desc), mismatch.desc)
		return
	case err != nil:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", messageOf(err))
		return
	}
	raw, exp, err := s.mintAccessToken(g)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "the token could not be issued")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": raw, "token_type": "Bearer", "expires_in": int64(exp.Sub(s.cfg.Clock.Now()).Seconds()),
		"scope": strings.Join(g.scopes, " "),
	})
}

// Binding failures. None of them spends the code: a verifier carries at
// least 256 bits, so it cannot be guessed in the code's short lifetime, and
// spending the code would let anyone who saw it deny the sign-in.
const (
	errOtherClient   = "the code was issued to another client"
	errRedirect      = "redirect_uri does not match the one in the authorization request"
	errVerifier      = "the PKCE code verifier does not match the code challenge"
	errOtherResource = "the resource does not match the one the code was issued for"
)

// bindingFailure is a redemption that does not match its grant.
type bindingFailure struct{ desc string }

func (e *bindingFailure) Error() string { return e.desc }

// bindingMismatch checks that the redemption matches the grant exactly and
// returns a description of the first mismatch, or "".
func bindingMismatch(g codeGrant, r redemption) string {
	switch {
	case r.clientID != g.clientID:
		return errOtherClient
	case r.redirectURI != g.redirectURI:
		return errRedirect
	case !pkceMatches(g.challenge, r.verifier):
		return errVerifier
	case r.resource != "" && r.resource != g.resource:
		return errOtherResource
	}
	return ""
}

// bindingError is the RFC error code of a binding failure.
func bindingError(desc string) string {
	if desc == errOtherResource {
		return "invalid_target"
	}
	return "invalid_grant"
}

package fakeoidc

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
)

// maxFormBytes bounds a token request body.
const maxFormBytes = 64 << 10

// authorize is the authorization endpoint of both dialects. It never shows a
// page: it signs in as the current user and redirects back with a code.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, github bool) {
	q := r.URL.Query()
	clientID, redirectURI, state := q.Get("client_id"), q.Get("redirect_uri"), q.Get("state")
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clients[clientID]
	if !ok || !slices.Contains(client.RedirectURIs, redirectURI) {
		// An unknown client or redirect URI is never redirected to.
		oauthError(w, http.StatusBadRequest, "invalid_request", "unknown client or unregistered redirect_uri")
		return
	}
	back := func(params url.Values) {
		params.Set("state", state)
		http.Redirect(w, r, redirectURI+"?"+params.Encode(), http.StatusFound)
	}
	challenge, method := q.Get("code_challenge"), q.Get("code_challenge_method")
	switch {
	case !github && q.Get("response_type") != "code":
		back(url.Values{"error": {"unsupported_response_type"}})
		return
	case challenge != "" && method != "S256":
		back(url.Values{"error": {"invalid_request"}, "error_description": {"only S256 PKCE is supported"}})
		return
	case s.current == "":
		back(url.Values{"error": {"login_required"}})
		return
	}
	now := s.clock.Now()
	if (q.Get("prompt") == "login" || q.Get("max_age") == "0") && !s.faults.IgnoreReauth {
		s.authAt[s.current] = now
	}
	code, err := randomString(24)
	if err != nil {
		back(url.Values{"error": {"server_error"}})
		return
	}
	s.pruneLocked()
	s.codes[code] = grant{clientID: clientID, redirectURI: redirectURI, subject: s.current, nonce: q.Get("nonce"),
		challenge: challenge, github: github, expires: now.Add(codeLifetime)}
	back(url.Values{"code": {code}})
}

// token is the token endpoint of both dialects. A code is single-use: it is
// removed before any check, so a failed or replayed exchange cannot retry it.
func (s *Server) token(w http.ResponseWriter, r *http.Request, github bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "malformed form")
		return
	}
	clientID, secret, basic := r.BasicAuth()
	if !basic {
		clientID, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if gt := r.PostForm.Get("grant_type"); gt != "" && gt != "authorization_code" {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code is supported")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	code := r.PostForm.Get("code")
	g, ok := s.codes[code]
	delete(s.codes, code)
	now := s.clock.Now()
	client, known := s.clients[clientID]
	switch {
	case !known || (client.Secret != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(client.Secret)) != 1):
		oauthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	case !ok || g.github != github || g.clientID != clientID || !now.Before(g.expires):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown, used or expired code")
		return
	case r.PostForm.Has("redirect_uri") && r.PostForm.Get("redirect_uri") != g.redirectURI:
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match")
		return
	case g.challenge != "" && !pkceMatches(g.challenge, r.PostForm.Get("code_verifier")):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	access, err := randomString(24)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "token generation failed")
		return
	}
	s.accessToken[access] = accessGrant{subject: g.subject, expires: now.Add(DefaultTokenLifetime)}
	resp := map[string]any{"access_token": access, "token_type": "bearer", "scope": ""}
	if !github {
		idToken, err := s.signIDToken(g, s.users[g.subject], now)
		if err != nil {
			oauthError(w, http.StatusInternalServerError, "server_error", "signing failed")
			return
		}
		resp["id_token"] = idToken
		resp["expires_in"] = int(DefaultTokenLifetime.Seconds())
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

// pkceMatches checks an S256 code verifier against its challenge.
func pkceMatches(challenge, verifier string) bool {
	if verifier == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

// pruneLocked drops expired codes and access tokens so memory stays bounded.
func (s *Server) pruneLocked() {
	now := s.clock.Now()
	for k, g := range s.codes {
		if !now.Before(g.expires) {
			delete(s.codes, k)
		}
	}
	for k, a := range s.accessToken {
		if !now.Before(a.expires) {
			delete(s.accessToken, k)
		}
	}
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

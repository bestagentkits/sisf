package fakeoidc

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Paths of the Google-style dialect, relative to the issuer.
const (
	DiscoveryPath     = "/.well-known/openid-configuration"
	OIDCAuthorizePath = "/o/oauth2/v2/auth"
	OIDCTokenPath     = "/token"
	JWKSPath          = "/oauth2/v3/certs"
)

// Paths of the GitHub-style dialect, relative to the issuer. The REST API
// lives under GitHubAPIPrefix.
const (
	GitHubAuthorizePath = "/login/oauth/authorize"
	GitHubTokenPath     = "/login/oauth/access_token"
	GitHubAPIPrefix     = "/api"
)

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+DiscoveryPath, s.discovery)
	mux.HandleFunc("GET "+JWKSPath, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.jwks()) })
	mux.HandleFunc("GET "+OIDCAuthorizePath, func(w http.ResponseWriter, r *http.Request) { s.authorize(w, r, false) })
	mux.HandleFunc("POST "+OIDCTokenPath, func(w http.ResponseWriter, r *http.Request) { s.token(w, r, false) })
	mux.HandleFunc("GET "+GitHubAuthorizePath, func(w http.ResponseWriter, r *http.Request) { s.authorize(w, r, true) })
	mux.HandleFunc("POST "+GitHubTokenPath, func(w http.ResponseWriter, r *http.Request) { s.token(w, r, true) })
	mux.HandleFunc("GET "+GitHubAPIPrefix+"/user", s.githubUser)
	mux.HandleFunc("GET "+GitHubAPIPrefix+"/repos/{owner}/{repo}/collaborators/{login}/permission", s.githubPermission)
	s.controlRoutes(mux)
	return mux
}

// discovery is the OpenID Provider metadata document.
func (s *Server) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                s.issuer,
		"authorization_endpoint":                s.issuer + OIDCAuthorizePath,
		"token_endpoint":                        s.issuer + OIDCTokenPath,
		"jwks_uri":                              s.issuer + JWKSPath,
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid", "email", "profile"},
		"claims_supported":                      []string{"aud", "auth_time", "email", "email_verified", "exp", "iat", "iss", "name", "nonce", "sub"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

// bearerSubject returns the user an access token was issued to.
func (s *Server) bearerSubject(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || (!strings.EqualFold(scheme, "Bearer") && !strings.EqualFold(scheme, "token")) {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accessToken[strings.TrimSpace(token)]
	if !ok || !s.clock.Now().Before(a.expires) {
		return "", false
	}
	return a.subject, true
}

// githubUser is GET /user for the user an access token belongs to.
func (s *Server) githubUser(w http.ResponseWriter, r *http.Request) {
	subject, ok := s.bearerSubject(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	s.mu.Lock()
	u := s.users[subject]
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"id": u.GitHubID, "login": u.Login, "name": u.Name, "email": u.Email})
}

// githubPermission is the collaborator permission API. It stands for a call
// made with the factory's installation token, so any non-empty bearer is
// accepted: login tokens and installation tokens are separate credentials,
// and the fake does not model installation tokens.
func (s *Server) githubPermission(w http.ResponseWriter, r *http.Request) {
	if _, token, ok := strings.Cut(r.Header.Get("Authorization"), " "); !ok || strings.TrimSpace(token) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Requires authentication"})
		return
	}
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	s.mu.Lock()
	perm, ok := s.perms[repo+"|"+r.PathValue("login")]
	s.mu.Unlock()
	if !ok {
		perm = "none"
	}
	writeJSON(w, http.StatusOK, map[string]string{"permission": perm})
}

// controlRoutes are the /_sim endpoints for tests and operators that drive
// the fake over HTTP.
func (s *Server) controlRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /_sim/users", func(w http.ResponseWriter, r *http.Request) {
		var u User
		if decodeControl(w, r, &u) && requireField(w, u.Subject, "subject") {
			s.AddUser(u)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("POST /_sim/sign-in", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Subject string `json:"subject"`
		}
		if decodeControl(w, r, &in) {
			if !s.SignInAs(in.Subject) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown subject " + strconv.Quote(in.Subject)})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("POST /_sim/clients", func(w http.ResponseWriter, r *http.Request) {
		var c Client
		if decodeControl(w, r, &c) && requireField(w, c.ID, "client_id") {
			s.AddClient(c)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("POST /_sim/faults", func(w http.ResponseWriter, r *http.Request) {
		var f Faults
		if decodeControl(w, r, &f) {
			s.SetFaults(f)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("POST /_sim/permissions", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Repo, Login, Permission string }
		if decodeControl(w, r, &in) && requireField(w, in.Repo, "repo") && requireField(w, in.Login, "login") {
			s.SetRepoPermission(in.Repo, in.Login, in.Permission)
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func decodeControl(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxFormBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed JSON body"})
		return false
	}
	return true
}

func requireField(w http.ResponseWriter, value, name string) bool {
	if value == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": name + " is required"})
		return false
	}
	return true
}

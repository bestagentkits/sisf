package auth

import (
	"net/http"
	"strconv"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// HTTP routes served by Handler. {provider} is a configured provider name;
// each provider's redirect URL must point at its CallbackPath.
const (
	LoginPath    = "/auth/login/{provider}"
	CallbackPath = "/auth/callback/{provider}"
	StepUpPath   = "/auth/step-up/{provider}"
	LogoutPath   = "/auth/logout"
	SessionPath  = "/auth/session"
	TokensPath   = "/auth/tokens"
	TokenPath    = "/auth/tokens/{id}"
	// StepUpActionField and StepUpTargetField name, in a step-up form or
	// query, the one call the confirmation is for.
	StepUpActionField = "action"
	StepUpTargetField = "target"
	flowPath          = "/auth/"
	maxBodyBytes      = 16 << 10
)

// Handler serves sign-in, step-up, sign-out, the session view and personal
// access token management. Mount it so it receives paths under /auth/.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+LoginPath, s.handleLogin)
	mux.HandleFunc("GET "+CallbackPath, s.handleCallback)
	mux.HandleFunc("POST "+StepUpPath, s.handleStepUp)
	mux.HandleFunc("POST "+LogoutPath, s.handleLogout)
	mux.HandleFunc("GET "+SessionPath, s.handleSession)
	mux.HandleFunc("GET "+TokensPath, s.handleListTokens)
	mux.HandleFunc("POST "+TokensPath, s.handleCreateToken)
	mux.HandleFunc("DELETE "+TokenPath, s.handleRevokeToken)
	return mux
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	authURL, binding, err := s.beginFlow(r.PathValue("provider"), LocalReturnPath(r.URL.Query().Get("return_to")), "", StepUpTarget{})
	if err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, s.cookie(r, FlowCookie, binding, flowPath, int(flowTTL.Seconds())))
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleStepUp starts a confirmation of the signed-in person for one call:
// the action and target fields name it, from the form or the query. A
// step-up that does not name one call is refused.
func (s *Service) handleStepUp(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	ss, err := s.sessionFromRequest(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	target, err := parseStepUpTarget(r.FormValue(StepUpActionField), r.FormValue(StepUpTargetField))
	if err != nil {
		writeError(w, err)
		return
	}
	authURL, binding, err := s.beginFlow(r.PathValue("provider"), LocalReturnPath(r.URL.Query().Get("return_to")), ss.key, target)
	if err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, s.cookie(r, FlowCookie, binding, flowPath, int(flowTTL.Seconds())))
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

func (s *Service) handleCallback(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, s.cookie(r, FlowCookie, "", flowPath, -1))
	q := r.URL.Query()
	if q.Get("error") != "" {
		writeError(w, unauthenticated("the provider did not sign the person in"))
		return
	}
	var binding, sessionKey string
	if c, err := r.Cookie(FlowCookie); err == nil {
		binding = c.Value
	}
	var previous string
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		previous = c.Value
		sessionKey = digest(c.Value)
	}
	res, err := s.completeFlow(r.Context(), r.PathValue("provider"), q.Get("state"), q.Get("code"), binding, sessionKey)
	if err != nil {
		writeError(w, err)
		return
	}
	if res.session != nil {
		// A sign-in always gets a new session ID; one the browser held
		// before is ended, so a planted session cannot be inherited.
		if previous != "" {
			s.mu.Lock()
			if old, ok := s.sessions[digest(previous)]; ok {
				s.dropSessionLocked(old)
			}
			s.mu.Unlock()
		}
		http.SetCookie(w, s.cookie(r, SessionCookie, res.cookie, "/", int(s.cfg.SessionTTL.Seconds())))
	}
	http.Redirect(w, r, res.returnTo, http.StatusSeeOther)
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	ss, err := s.sessionFromRequest(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	_ = s.RevokeSession(ss.handle)
	http.SetCookie(w, s.cookie(r, SessionCookie, "", "/", -1))
	w.WriteHeader(http.StatusNoContent)
}

// sessionView is the JSON answer of GET /auth/session. It includes the CSRF
// token for same-origin scripts; it never includes the session ID.
type sessionView struct {
	ActorID   string    `json:"actor_id"`
	SessionID string    `json:"session_id"`
	Issuer    string    `json:"issuer"`
	Subject   string    `json:"subject"`
	Login     string    `json:"login,omitempty"`
	Name      string    `json:"name,omitempty"`
	Email     string    `json:"email,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	CSRFToken string    `json:"csrf_token"`
}

func (s *Service) handleSession(w http.ResponseWriter, r *http.Request) {
	ss, err := s.sessionFromRequest(r, false)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, sessionView{ActorID: ss.actorID, SessionID: ss.handle, Issuer: ss.ident.Issuer,
		Subject: ss.ident.Subject, Login: ss.ident.Login, Name: ss.ident.Name, Email: ss.ident.Email,
		ExpiresAt: ss.expires, CSRFToken: ss.csrf})
}

func (s *Service) handleListTokens(w http.ResponseWriter, r *http.Request) {
	ss, err := s.sessionFromRequest(r, false)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": s.Tokens(ss.actorID)})
}

// Fields of the token form POST /auth/tokens reads. scopes repeats, once
// per scope.
const (
	TokenNameField   = "name"
	TokenScopesField = "scopes"
	TokenTTLField    = "ttl_seconds"
)

// handleCreateToken mints a personal access token from the person's own
// form post. Minting spends a step-up, so a script's fetch, a JSON body or
// a token is refused before anything is spent; the step-up must name this
// exact request (PATRequest.StepUpTarget).
func (s *Service) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	ss, err := s.sessionFromRequest(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	if !NavigatedFormPost(r) {
		writeError(w, core.NewError(core.CodePermissionDenied,
			"a token is minted only from a form you submit in the browser; a script cannot mint one"))
		return
	}
	req, err := patRequestFromForm(r)
	if err != nil {
		writeError(w, err)
		return
	}
	issued, err := s.IssuePAT(ss.actor(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"token": string(issued.Token.Reveal()), "info": issued.TokenInfo})
}

// patRequestFromForm reads the token form. A field it does not know is
// refused, so a form cannot carry more than it shows.
func patRequestFromForm(r *http.Request) (PATRequest, error) {
	if err := r.ParseForm(); err != nil {
		return PATRequest{}, invalidArgument("the token form could not be read")
	}
	form := r.PostForm
	for key := range form {
		switch key {
		case TokenNameField, TokenScopesField, TokenTTLField, CSRFFormField:
		default:
			return PATRequest{}, invalidArgument("the token form has no field " + key)
		}
	}
	if len(form[TokenNameField]) > 1 || len(form[TokenTTLField]) > 1 {
		return PATRequest{}, invalidArgument("the token form names one name and one lifetime")
	}
	var ttl int64
	if raw := form.Get(TokenTTLField); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 || n > int64(MaxPATTTL/time.Second) {
			return PATRequest{}, invalidArgument("a token may live at most " + MaxPATTTL.String())
		}
		ttl = n
	}
	return PATRequest{Name: form.Get(TokenNameField), Scopes: form[TokenScopesField], TTL: time.Duration(ttl) * time.Second}, nil
}

func (s *Service) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	ss, err := s.sessionFromRequest(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	s.mu.Lock()
	t, ok := s.tokenByID[r.PathValue("id")]
	owned := ok && t.info.ActorID == ss.actorID
	s.mu.Unlock()
	if !owned {
		writeError(w, core.NewError(core.CodeNotFound, "no such token"))
		return
	}
	if err := s.RevokeToken(t.info.ID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

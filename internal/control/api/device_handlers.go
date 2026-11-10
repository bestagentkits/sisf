package api

import (
	"mime"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/pkg/core"
)

// maxSmallBody bounds the bodies of the device endpoints.
const maxSmallBody = 16 << 10

var clientNamePattern = regexp.MustCompile(`^[A-Za-z0-9 ._@:/-]{0,64}$`)

// deviceStart opens a login request. It needs no credentials: all it yields
// is a code a signed-in person may approve, and nothing is issued before that.
func (h *handler) deviceStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if err := decodeBody(w, r, maxSmallBody, &in); err != nil {
		h.fail(w, err)
		return
	}
	if !clientNamePattern.MatchString(in.Name) {
		h.fail(w, invalid("the client name is at most 64 letters, digits and . _ @ : / -"))
		return
	}
	scopes := h.scopes
	if len(in.Scopes) > 0 {
		for _, scope := range in.Scopes {
			if !slices.Contains(h.scopes, scope) {
				h.fail(w, invalid("scope "+scope+" is not a scope of any action"))
				return
			}
		}
		scopes = slices.Compact(slices.Sorted(slices.Values(in.Scopes)))
	}
	if len(scopes) == 0 {
		h.fail(w, core.NewError(core.CodeNotSupported, "the factory has no action a token could be scoped to"))
		return
	}
	deviceCode, g, err := h.devices.start(in.Name, scopes)
	if err != nil {
		h.fail(w, err)
		return
	}
	verify := h.baseURL(r) + DeviceVerifyPath
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               deviceCode,
		"user_code":                 formatUserCode(g.userCode),
		"verification_uri":          verify,
		"verification_uri_complete": verify + "?user_code=" + formatUserCode(g.userCode),
		"expires_in":                int(h.cfg.DeviceCodeTTL / time.Second),
		"interval":                  int(h.cfg.PollInterval / time.Second),
		"scopes":                    scopes,
	})
}

// baseURL is the public base of the control server.
func (h *handler) baseURL(r *http.Request) string {
	if h.cfg.ExternalURL != "" {
		return h.cfg.ExternalURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// deviceToken is what the CLI polls. It answers 202 while nobody approved
// and hands the token over once.
func (h *handler) deviceToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceCode string `json:"device_code"`
	}
	if err := decodeBody(w, r, maxSmallBody, &in); err != nil {
		h.fail(w, err)
		return
	}
	if in.DeviceCode == "" || len(in.DeviceCode) > 128 {
		h.fail(w, invalid("device_code is required"))
		return
	}
	token, info, pending, err := h.devices.poll(in.DeviceCode)
	switch {
	case err != nil:
		h.fail(w, err)
	case pending:
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "pending", "interval": int(h.cfg.PollInterval / time.Second)})
	default:
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"status": "approved", "token": string(token.Reveal()),
			"token_type": "Bearer", "expires_at": info.ExpiresAt, "scopes": info.Scopes})
	}
}

// deviceApprove lets the person behind a browser session approve a login
// request. The token is minted from that session, so it carries the session's
// step-up requirement and never more authority than the person holds; a
// bearer token cannot approve because it has no session to mint from.
func (h *handler) deviceApprove(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.ActorFrom(r.Context())
	if !ok {
		h.fail(w, core.NewError(core.CodeUnauthenticated, "the caller could not be identified"))
		return
	}
	asForm := isFormRequest(r)
	var userCode string
	if asForm {
		userCode = r.PostFormValue("user_code")
	} else {
		var in struct {
			UserCode string `json:"user_code"`
		}
		if err := decodeBody(w, r, maxSmallBody, &in); err != nil {
			h.fail(w, err)
			return
		}
		userCode = in.UserCode
	}
	view, err := h.approve(actor, userCode)
	switch {
	case asForm:
		h.renderApproval(w, r, userCode, err)
	case err != nil:
		h.fail(w, err)
	default:
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"approved": true, "scopes": view.scopes, "expires_at": view.expires})
	}
}

// approve claims the request, mints the token and parks it for the CLI.
func (h *handler) approve(actor core.Actor, userCode string) (grant, error) {
	if len(userCode) > 32 {
		return grant{}, invalid("the login code is malformed")
	}
	key, g, err := h.devices.claim(userCode)
	if err != nil {
		return grant{}, err
	}
	issued, err := h.cfg.Identity.IssuePAT(actor, auth.PATRequest{Name: tokenName(g.name), Scopes: g.scopes, TTL: h.cfg.DeviceTokenTTL})
	if err != nil {
		h.devices.release(key)
		return grant{}, err
	}
	h.devices.approve(key, issued)
	g.expires = issued.ExpiresAt
	return g, nil
}

func tokenName(client string) string {
	if client == "" {
		return "cli login"
	}
	return "cli login: " + client
}

func isFormRequest(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/x-www-form-urlencoded"
}

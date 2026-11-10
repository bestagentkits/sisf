package main

import (
	"crypto/subtle"
	"html"
	"net/http"
	"os"
	"strings"

	"github.com/bestagentkits/sisf/internal/testkit/fakeoidc"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The sign-in gate of the fake identity provider of a development install.
//
// The fake has no login page: whoever reaches its authorize endpoint is
// signed in, as the Owner by default, and its /_sim endpoints choose who
// that is. In the simulation nothing else runs on the machine, but a
// development install runs real agents as local processes, which can reach
// every loopback port. So there the fake signs in only a browser that first
// proved it holds the sign-in secret `sisf up` writes to a file of the
// install's control secrets: the operator reads it, an agent's environment,
// workspace and home never hold it.
const (
	// simUnlockPath shows the unlock form (GET) and takes the secret (POST).
	simUnlockPath = "/_sim/unlock"
	// simUnlockCookie carries the secret on every later request of the browser.
	simUnlockCookie = "sisf_sim_unlock"
	// simUnlockField is the form field of the secret.
	simUnlockField = "secret"
	// minSignInSecret is the shortest secret accepted: 128 bits as base64.
	minSignInSecret = 22
)

// readSignInSecret reads the sign-in secret from path.
func readSignInSecret(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", core.NewError(core.CodeUnavailable, "the sign-in secret cannot be read from --sign-in-secret-file").WithCause(err)
	}
	secret := strings.TrimSpace(string(raw))
	if len(secret) < minSignInSecret || strings.ContainsAny(secret, " \t\r\n;,\"\\") {
		return "", core.NewError(core.CodeInvalidArgument, "the sign-in secret in --sign-in-secret-file is too short or holds characters a cookie cannot carry")
	}
	return secret, nil
}

// signInGate refuses every sign-in at next, and every /_sim endpoint, to a
// browser that has not unlocked it with secret. Discovery, keys, the token
// exchange and the user API stay open: the control server calls them, and
// a code to exchange comes only from a gated sign-in.
func signInGate(next http.Handler, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == simUnlockPath {
			unlock(w, r, secret)
			return
		}
		if gatedPath(r.URL.Path) && !unlocked(r, secret) {
			http.Error(w, "this development install's identity provider signs in only a browser unlocked at "+simUnlockPath+
				" with the sign-in secret `sisf up` printed the location of", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// gatedPath reports whether path signs someone in or steers who it is.
func gatedPath(path string) bool {
	return path == fakeoidc.OIDCAuthorizePath || path == fakeoidc.GitHubAuthorizePath || strings.HasPrefix(path, "/_sim/")
}

// unlocked reports whether the request carries the secret in its cookie.
func unlocked(r *http.Request, secret string) bool {
	c, err := r.Cookie(simUnlockCookie)
	return err == nil && equalSecret(c.Value, secret)
}

func equalSecret(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// unlock serves the unlock form and, given the right secret, sets the
// cookie that unlocks sign-in for this browser.
func unlock(w http.ResponseWriter, r *http.Request, secret string) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><title>Unlock sign-in</title>
<form method="post" action="` + html.EscapeString(simUnlockPath) + `">
<label>Sign-in secret (from the file <code>sisf up</code> named) <input name="` + simUnlockField + `" type="password" autocomplete="off"></label>
<button>Unlock</button></form>`))
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		if err := r.ParseForm(); err != nil || !equalSecret(r.PostForm.Get(simUnlockField), secret) {
			http.Error(w, "wrong sign-in secret", http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: simUnlockCookie, Value: secret, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("sign-in unlocked for this browser; return to the factory and sign in\n"))
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
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
//
// The browser keeps a proof of the secret, never the secret: an HMAC of a
// fixed label under the secret, in an HttpOnly, SameSite=Strict cookie.
// Cookies are not scoped by port, so that cookie also goes with requests to
// every other loopback port, where an agent may serve a page; the secret
// itself is never sent anywhere. And since such a page is same-site with
// the fake, every POST to /_sim must come from the fake's own origin, by
// its Origin header, and name the fake's own host, so a page served
// elsewhere cannot steer who signs in through the operator's browser.
const (
	// simUnlockPath shows the unlock form (GET) and takes the secret (POST).
	simUnlockPath = "/_sim/unlock"
	// simUnlockCookie carries the proof of the secret on every later
	// request of the browser.
	simUnlockCookie = "sisf_sim_unlock"
	// simUnlockLabel is what the proof is an HMAC of.
	simUnlockLabel = "sisf sim oidc: sign-in unlocked"
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
// browser that has not unlocked it with secret, and every POST to /_sim
// from anywhere but one of origins, the fake's own. Discovery, keys, the
// token exchange and the user API stay open: the control server calls
// them, and a code to exchange comes only from a gated sign-in.
func signInGate(next http.Handler, secret string, origins []string) http.Handler {
	proof := unlockProof(secret)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/_sim/") && !fromOwnOrigin(r, origins) {
			http.Error(w, "this development install's identity provider takes a POST to /_sim only from its own origin", http.StatusForbidden)
			return
		}
		if r.URL.Path == simUnlockPath {
			unlock(w, r, secret, proof)
			return
		}
		if gatedPath(r.URL.Path) && !unlocked(r, proof) {
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

// unlockProof is the cookie value that shows a browser unlocked sign-in:
// an HMAC of a fixed label under the secret, from which the secret cannot
// be recovered.
func unlockProof(secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(simUnlockLabel)) // a hash never fails to write
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// unlocked reports whether the request carries the proof in its cookie.
func unlocked(r *http.Request, proof string) bool {
	c, err := r.Cookie(simUnlockCookie)
	return err == nil && equalSecret(c.Value, proof)
}

func equalSecret(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// fromOwnOrigin reports whether r names one of origins in its Origin header
// and that origin's host in its Host header. A browser sends Origin with
// every POST, so a request without one is refused too.
func fromOwnOrigin(r *http.Request, origins []string) bool {
	origin := r.Header.Values("Origin")
	if len(origin) != 1 || !slices.Contains(origins, origin[0]) {
		return false
	}
	u, err := url.Parse(origin[0])
	return err == nil && strings.EqualFold(r.Host, u.Host)
}

// ownOrigins are the origins a browser reaches the fake at: the issuer's,
// the listener's and, on a loopback listener, the same port on localhost.
func ownOrigins(issuer string, listen net.Addr) []string {
	var out []string
	add := func(origin string) {
		if !slices.Contains(out, origin) {
			out = append(out, origin)
		}
	}
	if u, err := url.Parse(issuer); err == nil && u.Host != "" {
		add(u.Scheme + "://" + u.Host)
	}
	host, port, err := net.SplitHostPort(listen.String())
	if err != nil {
		return out
	}
	ip := net.ParseIP(host)
	if ip != nil && !ip.IsUnspecified() {
		add("http://" + net.JoinHostPort(host, port))
	}
	if ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		add("http://" + net.JoinHostPort("localhost", port))
		add("http://" + net.JoinHostPort("127.0.0.1", port))
	}
	return out
}

// unlock serves the unlock form and, given the right secret, sets the
// cookie that carries the proof of it for this browser.
func unlock(w http.ResponseWriter, r *http.Request, secret, proof string) {
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
		http.SetCookie(w, &http.Cookie{Name: simUnlockCookie, Value: proof, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("sign-in unlocked for this browser; return to the factory and sign in\n"))
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

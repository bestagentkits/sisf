package control

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// MinTokenBytes is the shortest bootstrap token the server accepts. The
// installer generates a random token far longer than this; the floor only
// stops a configuration mistake from leaving the Owner API guessable.
const MinTokenBytes = 32

// ownerToken checks presented tokens against the bootstrap Owner token. It
// keeps a digest, not the token, and compares digests in constant time, so
// neither the content nor the length of the token leaks through timing.
type ownerToken struct {
	digest [sha256.Size]byte
}

func newOwnerToken(token core.SecretValue) (*ownerToken, error) {
	raw := token.Reveal()
	if len(raw) < MinTokenBytes {
		return nil, core.NewError(core.CodeInvalidArgument, "the Owner token must be at least 32 bytes")
	}
	return &ownerToken{digest: sha256.Sum256(raw)}, nil
}

// matches reports whether candidate is the Owner token.
func (t *ownerToken) matches(candidate string) bool {
	d := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(d[:], t.digest[:]) == 1
}

// bearerToken extracts the token from an "Authorization: Bearer" header.
func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// requireOwner guards an API route: it runs next only for a request that
// carries the Owner token as a bearer credential. Cookies are not accepted
// here, so the API has no ambient credential a web page could borrow.
func (s *Server) requireOwner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		presented := bearerToken(r)
		if presented == "" || !s.token.matches(presented) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="sisf-control"`)
			s.writeAPIError(w, r, core.NewError(core.CodeUnauthenticated,
				"send the Owner token as an Authorization: Bearer header"))
			return
		}
		next(w, r)
	}
}

package edge

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxAdminBody bounds a request to the admin handler.
const maxAdminBody = 16 << 10

// CountsAnswer is the body of GET /counters.
type CountsAnswer struct {
	// WindowSeconds is 0 for totals since the edge started.
	WindowSeconds int64             `json:"window_seconds"`
	Revisions     map[string]Counts `json:"revisions"`
}

// AdminOption changes what an admin handler accepts.
type AdminOption func(*adminTokens)

type adminTokens struct{ admin, counts string }

// WithCountsToken adds a second, read-only bearer token that authorizes GET
// /counters and nothing else, for a reader of the counters such as the
// control zone. It must differ from the admin token.
func WithCountsToken(token string) AdminOption {
	return func(t *adminTokens) { t.counts = token }
}

// NewAdminHandler serves the control surface of a splitter: GET and PUT
// /split, and GET /counters with an optional ?window=10m. It is meant for a
// listener of its own, never the public one, and every request must carry
// the admin bearer token, or the counters token for GET /counters only. An
// empty admin token is refused so the surface cannot be left open by
// accident, and a counters token equal to it is refused so reading the
// counters never grants moving traffic.
func NewAdminHandler(s *Splitter, token string, opts ...AdminOption) (http.Handler, error) {
	tokens := adminTokens{admin: token}
	for _, o := range opts {
		o(&tokens)
	}
	if tokens.admin == "" {
		return nil, errors.New("the admin surface needs a token")
	}
	if tokens.counts != "" && subtle.ConstantTimeCompare([]byte(tokens.counts), []byte(tokens.admin)) == 1 {
		return nil, errors.New("the counters token must differ from the admin token")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /split", func(w http.ResponseWriter, r *http.Request) {
		cur, _ := s.Split(r.Context())
		writeJSON(w, http.StatusOK, cur)
	})
	mux.HandleFunc("PUT /split", func(w http.ResponseWriter, r *http.Request) {
		var next Split
		dec := json.NewDecoder(io.LimitReader(r.Body, maxAdminBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&next); err != nil || dec.More() {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body must be one split object"})
			return
		}
		if err := next.validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := s.SetSplit(r.Context(), next); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the split could not be applied"})
			return
		}
		cur, _ := s.Split(r.Context())
		writeJSON(w, http.StatusOK, cur)
	})
	mux.HandleFunc("GET /counters", func(w http.ResponseWriter, r *http.Request) {
		var window time.Duration
		if raw := r.URL.Query().Get("window"); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "window is a duration such as 10m"})
				return
			}
			window = d
		}
		counts, err := s.Counts(r.Context(), window)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, CountsAnswer{WindowSeconds: int64(window / time.Second), Revisions: counts})
	})
	return authorized(tokens, mux), nil
}

// authorized lets a request through with the exact admin token, and lets
// the counters token through to GET /counters only.
func authorized(tokens adminTokens, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case ok && subtle.ConstantTimeCompare([]byte(got), []byte(tokens.admin)) == 1:
			next.ServeHTTP(w, r)
		case ok && tokens.counts != "" && subtle.ConstantTimeCompare([]byte(got), []byte(tokens.counts)) == 1:
			if r.Method != http.MethodGet || r.URL.Path != "/counters" {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "the counters token reads the counters only"})
				return
			}
			next.ServeHTTP(w, r)
		default:
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a bearer token is required"})
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // a client that went away is not an error here
}

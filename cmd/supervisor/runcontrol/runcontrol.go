// Package runcontrol is how the supervisor's emergency stop reaches the
// agent zone across processes: the dispatcher serves a small admin route
// that cancels an agent run and revokes every secret grant of a run, and
// the supervisor calls it with a bearer token only the two hold.
//
// The route changes nothing but stopping: it cannot start a run or issue a
// grant.
package runcontrol

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Paths of the admin route.
const (
	PathCancel = "/admin/runs/cancel"
	PathRevoke = "/admin/runs/revoke"
)

// EnvToken names the environment variable holding the shared token.
const EnvToken = "SISF_DISPATCHER_ADMIN_TOKEN"

// Canceller cancels an agent run; agent.Backend is one.
type Canceller interface {
	Cancel(ctx context.Context, run agent.RunRef) error
}

// Revoker ends every secret grant of a run; the secret broker is one.
type Revoker interface {
	RevokeRun(ctx context.Context, run core.RunID) error
}

type revokeRequest struct {
	RunID core.RunID `json:"run_id"`
}

// Handler serves the admin route. A request without the token is refused
// with 401 and does nothing.
func Handler(token string, runs Canceller, secrets Revoker) (http.Handler, error) {
	if len(token) < 32 || runs == nil || secrets == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the run control route needs a token of 32 characters or more, the agent backend and the secret broker")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+PathCancel, func(w http.ResponseWriter, r *http.Request) {
		var ref agent.RunRef
		if !decode(w, r, &ref) || ref.RunID.Validate() != nil || ref.ID == "" {
			http.Error(w, "a run reference is required", http.StatusBadRequest)
			return
		}
		answer(w, runs.Cancel(r.Context(), ref))
	})
	mux.HandleFunc("POST "+PathRevoke, func(w http.ResponseWriter, r *http.Request) {
		var req revokeRequest
		if !decode(w, r, &req) || req.RunID.Validate() != nil {
			http.Error(w, "a run is required", http.StatusBadRequest)
			return
		}
		answer(w, secrets.RevokeRun(r.Context(), req.RunID))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		given, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}

func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(out) == nil
}

// answer writes 204, or the typed error's code with 409.
func answer(w http.ResponseWriter, err error) {
	if err != nil {
		http.Error(w, string(core.CodeOf(err)), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Client calls a dispatcher's admin route. It implements the emergency
// stop's RunCanceller and RunRevoker.
type Client struct {
	base  string
	token core.SecretValue
	http  *http.Client
}

// NewClient returns a client of the route at base.
func NewClient(base string, token core.SecretValue) (*Client, error) {
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") || token.IsZero() {
		return nil, core.NewError(core.CodeInvalidArgument, "the dispatcher admin URL must be http(s) and its token must be set")
	}
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: 10 * time.Second}}, nil
}

// Cancel asks the dispatcher to cancel an agent run.
func (c *Client) Cancel(ctx context.Context, run agent.RunRef) error {
	return c.post(ctx, PathCancel, run)
}

// RevokeRun asks the dispatcher to revoke every grant of run.
func (c *Client) RevokeRun(ctx context.Context, run core.RunID) error {
	return c.post(ctx, PathRevoke, revokeRequest{RunID: run})
}

func (c *Client) post(ctx context.Context, path string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return core.NewError(core.CodeInternal, "the request cannot be encoded").WithCause(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return core.NewError(core.CodeInvalidArgument, "the dispatcher admin request cannot be built")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+string(c.token.Reveal()))
	resp, err := c.http.Do(req)
	if err != nil {
		return core.NewError(core.CodeUnavailable, "the dispatcher admin route cannot be reached")
	}
	defer func() { _ = resp.Body.Close() }()
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusUnauthorized:
		return core.NewError(core.CodePermissionDenied, "the dispatcher refused the admin token")
	}
	return core.NewError(core.CodeUnavailable, "the dispatcher could not stop the run: "+strings.TrimSpace(string(detail)))
}

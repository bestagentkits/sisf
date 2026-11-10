package supervisor

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// statusPath is the one resource the admin socket serves.
const statusPath = "/status"

// maxStatusBytes bounds how much of a status answer a client reads.
const maxStatusBytes = 1 << 20

// Status is what the supervisor reports about itself. It holds digests, key
// ids and times only; nothing in it is a secret.
type Status struct {
	FactoryID core.FactoryID  `json:"factory_id"`
	StartedAt time.Time       `json:"started_at"`
	SelfCheck SelfCheckStatus `json:"self_check"`
	LKG       LKGStatus       `json:"lkg"`
	// HeldBuilds are the candidate builds the supervisor stopped retrying
	// after repeated outages; each waits for the Owner's retry of its
	// issue. HeldBuildsUnknown is set when they could not be read.
	HeldBuilds        []HeldBuild `json:"held_builds,omitempty"`
	HeldBuildsUnknown bool        `json:"held_builds_unknown,omitempty"`
}

// HeldBuild is one held candidate build. It names the error code of the
// last outage only, never its message.
type HeldBuild struct {
	IssueID  core.IssueRef  `json:"issue_id"`
	RunID    core.RunID     `json:"run_id"`
	Attempt  int            `json:"attempt"`
	Failures int            `json:"failures"`
	LastCode core.ErrorCode `json:"last_code"`
	Since    time.Time      `json:"since"`
}

// HeldBuildsReader lists the held candidate builds.
type HeldBuildsReader func(ctx context.Context) ([]HeldBuild, error)

// heldBuildsTimeout bounds how long a status answer waits for the list.
const heldBuildsTimeout = 3 * time.Second

// SelfCheckStatus is the evidence of the startup self-check. A supervisor
// that serves status at all has passed it; one that failed is not running.
type SelfCheckStatus struct {
	Passed bool `json:"passed"`
	// ExecutableDigest is what the supervisor measured of itself.
	ExecutableDigest core.Digest `json:"executable_digest"`
	// ManifestKeyID names the root key that signed the release manifest.
	ManifestKeyID    string    `json:"manifest_key_id"`
	ManifestIssuedAt time.Time `json:"manifest_issued_at"`
}

// LKGStatus says whether a last known good release is recorded, and which.
type LKGStatus struct {
	Recorded   bool                              `json:"recorded"`
	RecordedAt time.Time                         `json:"recorded_at,omitzero"`
	Units      map[manifest.UnitName]core.Digest `json:"units,omitempty"`
}

// newAdminHandler serves the status and nothing else. It registers a single
// read route, so every other path is not found and every other method on
// the status path is refused: no request on this socket can change the
// supervisor. A supervisor started with an emergency stop adds that one
// route, which can only make the factory stop, never let it run again.
func newAdminHandler(status func() Status) http.Handler {
	return newAdminMux(status, Options{})
}

// newAdminMux serves the status and the actions opts provides. Nothing else
// is routed: every other path is not found and every other method is
// refused.
func newAdminMux(status func() Status, opts Options) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+statusPath, func(w http.ResponseWriter, r *http.Request) {
		st := status()
		if opts.HeldBuilds != nil {
			ctx, cancel := context.WithTimeout(r.Context(), heldBuildsTimeout)
			held, err := opts.HeldBuilds(ctx)
			cancel()
			st.HeldBuilds, st.HeldBuildsUnknown = held, err != nil
		}
		writeAdminJSON(w, http.StatusOK, st)
	})
	if opts.EmergencyStop != nil {
		mux.Handle("POST "+EmergencyStopPath, emergencyStopHandler(opts.EmergencyStop))
	}
	if opts.FreezeStatus != nil {
		mux.Handle("GET "+EmergencyStopPath, freezeStatusHandler(opts.FreezeStatus))
	}
	if opts.Rollback != nil {
		mux.Handle("POST "+RollbackPath, rollbackHandler(opts.Rollback))
	}
	return mux
}

// FetchStatus asks the supervisor listening on addr for its status. The
// address goes through the same rule as the listener's, so this client
// cannot be pointed at another machine. A supervisor that does not answer
// is UNAVAILABLE.
func FetchStatus(ctx context.Context, addr string) (Status, error) {
	var status Status
	err := callAdmin(ctx, addr, http.MethodGet, statusPath, nil, &status)
	return status, err
}

// callAdmin sends one request to the supervisor listening on addr and
// decodes its JSON answer into out. The address goes through the same rule
// as the listener's, so this client cannot be pointed at another machine. A
// supervisor that does not answer is UNAVAILABLE; a refusal it answers with
// keeps the error code the supervisor gave.
func callAdmin(ctx context.Context, addr, method, path string, body io.Reader, out any) error {
	target, err := parseAdminAddr(addr)
	if err != nil {
		return err
	}
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, target.network, target.address)
		},
	}
	defer transport.CloseIdleConnections()
	// The host in the URL is a label; the dialer above decides where to connect.
	request, err := http.NewRequestWithContext(ctx, method, "http://supervisor"+path, body)
	if err != nil {
		return newError(core.CodeInternal, "the admin request cannot be built").WithCause(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return newError(core.CodeUnavailable, "no supervisor answers on %s", target).WithCause(err)
	}
	defer func() { _ = response.Body.Close() }() // the answer was read or abandoned already
	reader := io.LimitReader(response.Body, maxStatusBytes)
	if response.StatusCode != http.StatusOK {
		var refusal adminError
		if json.NewDecoder(reader).Decode(&refusal) == nil && refusal.Code != "" {
			return core.NewError(refusal.Code, refusal.Message)
		}
		return newError(core.CodeUnavailable, "the supervisor on %s answered %s", target, response.Status)
	}
	if err := json.NewDecoder(reader).Decode(out); err != nil {
		return newError(core.CodeUnavailable, "the supervisor on %s sent an unreadable answer", target).WithCause(err)
	}
	return nil
}

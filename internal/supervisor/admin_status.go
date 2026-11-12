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
}

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
// supervisor.
func newAdminHandler(status func() Status) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+statusPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A failed write means the client went away; there is nobody to tell.
		_ = json.NewEncoder(w).Encode(status())
	})
	return mux
}

// FetchStatus asks the supervisor listening on addr for its status. The
// address goes through the same rule as the listener's, so this client
// cannot be pointed at another machine. A supervisor that does not answer
// is UNAVAILABLE.
func FetchStatus(ctx context.Context, addr string) (Status, error) {
	target, err := parseAdminAddr(addr)
	if err != nil {
		return Status{}, err
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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://supervisor"+statusPath, nil)
	if err != nil {
		return Status{}, newError(core.CodeInternal, "the status request cannot be built").WithCause(err)
	}
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return Status{}, newError(core.CodeUnavailable, "no supervisor answers on %s", target).WithCause(err)
	}
	defer func() { _ = response.Body.Close() }() // the answer was read or abandoned already
	if response.StatusCode != http.StatusOK {
		return Status{}, newError(core.CodeUnavailable, "the supervisor on %s answered %s", target, response.Status)
	}
	var status Status
	if err := json.NewDecoder(io.LimitReader(response.Body, maxStatusBytes)).Decode(&status); err != nil {
		return Status{}, newError(core.CodeUnavailable, "the supervisor on %s sent an unreadable status", target).WithCause(err)
	}
	return status, nil
}

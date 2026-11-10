// Package deploy defines the contract for staging, switching, observing and
// rolling back a release of one runtime unit.
//
// A release is judged by what is observed running, never by the result of the
// call that asked for it. A backend that cannot switch versions with a way
// back, or cannot be observed, does not self-publish: the release holds.
package deploy

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
)

// SelfPublishAllowed reports whether a backend with these capabilities may
// publish without a person: it needs a verified switching method (blue-green
// or canary), rollback, an observable revision and metrics. Anything missing
// or merely claimed means hold.
func SelfPublishAllowed(caps core.CapabilitySet) bool {
	switching := caps.Supports(core.CapDeployBlueGreen) || caps.Supports(core.CapDeployCanary)
	return switching && caps.Supports(core.CapDeployRollback) &&
		caps.Supports(core.CapDeployObservedRevision) && caps.Supports(core.CapDeployMetrics)
}

// Artifact is an immutable, signed build of one unit. The trusted release
// supervisor rejects an artifact whose signature it cannot verify.
type Artifact struct {
	// Unit is the runtime unit name from the manifest, for example "app".
	Unit   string             `json:"unit"`
	Digest core.ReleaseDigest `json:"digest"`
	// Image is the digest-pinned reference the backend pulls or loads.
	Image     string         `json:"image"`
	Signature core.Signature `json:"signature"`
	// SBOMRef and ProvenanceRef point to the supply-chain documents of the build.
	SBOMRef       string `json:"sbom_ref,omitempty"`
	ProvenanceRef string `json:"provenance_ref,omitempty"`
}

// Validate requires a unit, a full digest, an image reference and a signature.
// It does not verify the signature.
func (a Artifact) Validate() error {
	if a.Unit == "" || a.Image == "" {
		return core.NewError(core.CodeInvalidArgument, "artifact needs a unit and an image reference")
	}
	if err := a.Digest.Validate(); err != nil {
		return err
	}
	return a.Signature.Validate()
}

// Revision is a staged or running version of a unit.
type Revision struct {
	Unit   string             `json:"unit"`
	Digest core.ReleaseDigest `json:"digest"`
	// ID is the backend's handle, for example a slot or a container name.
	ID string `json:"id,omitempty"`
}

// Preview is where a staged revision can be reached before it takes traffic.
type Preview struct {
	Revision Revision `json:"revision"`
	URL      string   `json:"url"`
}

// Metrics is what was measured for one revision over a window.
type Metrics struct {
	Revision     Revision  `json:"revision"`
	From         time.Time `json:"from"`
	To           time.Time `json:"to"`
	Requests     int64     `json:"requests"`
	ServerErrors int64     `json:"server_errors"`
	// Healthy is the result of the health check at the end of the window.
	Healthy bool `json:"healthy"`
	// CriticalErrors counts new critical-level log errors in the window.
	CriticalErrors int64 `json:"critical_errors"`
}

// ErrorRate returns the share of requests that failed on the server. ok is
// false when there were no requests: no traffic is not a zero error rate, and
// no conclusion may be drawn from it.
func (m Metrics) ErrorRate() (rate float64, ok bool) {
	if m.Requests <= 0 {
		return 0, false
	}
	return float64(m.ServerErrors) / float64(m.Requests), true
}

// Observed is what is actually running for a unit, read from the running
// system itself.
type Observed struct {
	Unit       string             `json:"unit"`
	Digest     core.ReleaseDigest `json:"digest"`
	Healthy    bool               `json:"healthy"`
	ObservedAt time.Time          `json:"observed_at"`
}

// Matches reports whether the observation shows rev running and healthy.
func (o Observed) Matches(rev Revision) bool {
	return o.Healthy && o.Unit == rev.Unit && o.Digest == rev.Digest
}

// Backend deploys one unit at a time. An ability a backend lacks is declared
// in Capabilities and refused with NOT_SUPPORTED; missing metrics or a broken
// adapter surface as RELEASE_HOLD and are never reported as success.
type Backend interface {
	Name() string
	Capabilities(ctx context.Context) (core.CapabilitySet, error)
	// Stage makes the artifact ready to run without sending it traffic.
	Stage(ctx context.Context, artifact Artifact) (Revision, error)
	Preview(ctx context.Context, rev Revision) (Preview, error)
	// Measure returns the metrics of rev over the most recent window.
	Measure(ctx context.Context, rev Revision, window time.Duration) (Metrics, error)
	// Promote sends trafficPercent of traffic to rev. 100 completes the
	// switch; a backend without traffic splitting accepts only 100.
	Promote(ctx context.Context, rev Revision, trafficPercent int) error
	// ObservedRevision reads what is running from the running system, never
	// from the result of an earlier call.
	ObservedRevision(ctx context.Context, unit string) (Observed, error)
	// Rollback switches the unit back to an earlier revision. It replaces
	// code and configuration only and never overwrites stored data.
	Rollback(ctx context.Context, to Revision) error
}

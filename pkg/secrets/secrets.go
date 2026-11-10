// Package secrets defines how secrets are referenced, resolved and handed to
// runs.
//
// Configuration and GitHub hold references only, never values. A run gets a
// secret solely through the broker, limited to one purpose, scope and time
// and revoked when the run is cancelled. Control-plane credentials and
// signing keys are of a class the broker cannot hand out at all.
package secrets

import (
	"context"
	"regexp"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
)

// Class says who may ever hold a secret.
type Class string

const (
	// ClassRun is a secret a run may use, such as a scoped provider key.
	ClassRun Class = "run"
	// ClassControl is a control-plane credential: the GitHub App key, Owner
	// tokens, webhook secrets. It never leaves Zone 3.
	ClassControl Class = "control"
	// ClassSigning is a signing key, usable only by the trusted release
	// supervisor and the approval service.
	ClassSigning Class = "signing"
)

// Valid reports whether c is a known class.
func (c Class) Valid() bool { return c == ClassRun || c == ClassControl || c == ClassSigning }

// BrokerableToRun reports whether a secret of this class may be granted to a
// run. Only run-scoped secrets may; an unknown class may not.
func (c Class) BrokerableToRun() bool { return c == ClassRun }

// SecretRef names a secret without containing it. It is what configuration,
// the dashboard and audit records show.
type SecretRef struct {
	// Provider is the configured secret provider, for example "local" or "env".
	Provider string `json:"provider" yaml:"provider"`
	Name     string `json:"name" yaml:"name"`
	Class    Class  `json:"class" yaml:"class"`
	// Zone is the zone that owns the secret; other zones cannot read it.
	Zone core.Zone `json:"zone" yaml:"zone"`
}

var (
	providerPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	namePattern     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`)
)

// String renders "provider:name". It never includes the value.
func (r SecretRef) String() string { return r.Provider + ":" + r.Name }

// Validate rejects references that are incomplete, could escape their
// provider's namespace, or are not owned by exactly one zone.
func (r SecretRef) Validate() error {
	switch {
	case !providerPattern.MatchString(r.Provider) || !namePattern.MatchString(r.Name):
		return core.NewError(core.CodeInvalidArgument, "secret ref needs a provider and a relative name without spaces or dot segments")
	case !r.Class.Valid():
		return core.NewError(core.CodeInvalidArgument, "secret ref must state its class")
	case r.Zone.Number() == 0:
		return core.NewError(core.CodeInvalidArgument, "secret ref must be owned by one of the three zones")
	}
	return nil
}

// SecretProvider resolves references to values. Values are stored encrypted
// or held by an external manager, never in the repository.
type SecretProvider interface {
	// Resolve returns NOT_FOUND for an unknown reference.
	Resolve(ctx context.Context, ref SecretRef) (core.SecretValue, error)
	// ListRefs lists references only; it has no way to return values.
	ListRefs(ctx context.Context) ([]SecretRef, error)
}

// Writer is implemented by providers that can store and remove secrets.
type Writer interface {
	Put(ctx context.Context, ref SecretRef, value core.SecretValue) error
	Delete(ctx context.Context, ref SecretRef) error
}

// GrantRequest asks the broker to let one run use one secret.
type GrantRequest struct {
	RunID   core.RunID `json:"run_id"`
	Ref     SecretRef  `json:"ref"`
	Purpose string     `json:"purpose"`
	// Scopes narrow what the secret may be used for.
	Scopes []string      `json:"scopes,omitempty"`
	TTL    time.Duration `json:"ttl"`
}

// Validate rejects malformed requests with INVALID_ARGUMENT and requests for
// a secret that may never reach a run with PERMISSION_DENIED.
func (g GrantRequest) Validate() error {
	if err := g.RunID.Validate(); err != nil {
		return err
	}
	if err := g.Ref.Validate(); err != nil {
		return err
	}
	switch {
	case g.Purpose == "" || g.TTL <= 0:
		return core.NewError(core.CodeInvalidArgument, "grant needs a purpose and a positive lifetime")
	case !g.Ref.Class.BrokerableToRun():
		return core.NewError(core.CodePermissionDenied, "secret "+g.Ref.String()+" may not be granted to a run")
	}
	return nil
}

// Grant is permission for one run to redeem one secret until it expires.
type Grant struct {
	ID        string     `json:"id"`
	RunID     core.RunID `json:"run_id"`
	Ref       SecretRef  `json:"ref"`
	Purpose   string     `json:"purpose"`
	Scopes    []string   `json:"scopes,omitempty"`
	ExpiresAt time.Time  `json:"expires_at"`
}

// Expired reports whether the grant may no longer be redeemed.
func (g Grant) Expired(now time.Time) bool { return !now.Before(g.ExpiresAt) }

// Broker hands secrets to runs. It is the only path by which a run obtains a
// secret, and it enforces GrantRequest.Validate itself.
type Broker interface {
	Issue(ctx context.Context, req GrantRequest) (Grant, error)
	// Redeem returns the value for a live grant, and EXPIRED once the grant
	// has run out or was revoked.
	Redeem(ctx context.Context, grantID string) (core.SecretValue, error)
	// RevokeRun ends every grant of the run at once: on cancel, on
	// completion and on emergency stop.
	RevokeRun(ctx context.Context, run core.RunID) error
}

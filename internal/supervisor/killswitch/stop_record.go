// Package killswitch stops a factory in an emergency. It works from the
// supervisor and from the sisf CLI with nothing but the operational store and
// the supervisor's own clients: it needs neither the control plane nor the
// dispatcher to be up, and neither of them can veto it.
//
// A stop is a document in the store. While it exists:
//
//   - no issue can be claimed: the switch holds the issue lease of every open
//     issue, so a dispatcher's claim meets LEASE_HELD, and an issue whose run
//     lost its lease is fenced off as soon as the switch takes the lease over;
//   - runs in flight are cancelled through the agent runtime and every
//     per-run secret grant is revoked;
//   - publishing is frozen: no release step promotes or records a release.
//
// Stopping is idempotent and safe to repeat; Enforce re-applies a stop to
// issues that arrived after it. Lifting is a separate, deliberate act.
package killswitch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Namespace and key of the stop document. The namespace is the control
// zone's, which the supervisor opens the store as.
const (
	Namespace = "killswitch"
	stopKey   = "stop"
)

// Holder is the lease owner the switch holds issues as.
const Holder = "killswitch"

// DefaultHoldTTL is how long one pass holds an issue. Enforce renews it, so
// it only matters when nothing enforces the stop any more.
const DefaultHoldTTL = time.Hour

// actorID is who the audit log names for the switch's own steps.
const actorID = "service:supervisor"

// Stop is the stop document.
type Stop struct {
	ID        string    `json:"id"`
	Actor     string    `json:"actor"`
	Reason    string    `json:"reason"`
	StoppedAt time.Time `json:"stopped_at"`
}

// RunCanceller cancels an agent run. An agent backend implements it.
type RunCanceller interface {
	Cancel(ctx context.Context, run agent.RunRef) error
}

// RunRevoker ends every secret grant of a run. The secret broker implements it.
type RunRevoker interface {
	RevokeRun(ctx context.Context, run core.RunID) error
}

// Config is what a Switch is built from. Runs, Secrets and Audit are
// optional: without them the stop reports the parts it could not do.
type Config struct {
	FactoryID core.FactoryID
	// State is the operational store, opened for the control zone.
	State   state.Store
	Clock   core.Clock
	Runs    RunCanceller
	Secrets RunRevoker
	Audit   core.AuditLog
	// HoldTTL is how long one pass holds an issue; DefaultHoldTTL when zero.
	HoldTTL time.Duration
}

// Switch is the emergency stop of one factory.
type Switch struct{ cfg Config }

// New returns a switch.
func New(cfg Config) (*Switch, error) {
	if err := cfg.FactoryID.Validate(); err != nil {
		return nil, err
	}
	if cfg.State == nil || cfg.Clock == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the emergency stop needs the state store and a clock")
	}
	if cfg.HoldTTL <= 0 {
		cfg.HoldTTL = DefaultHoldTTL
	}
	return &Switch{cfg: cfg}, nil
}

// current reads the stop in force; found is false when there is none.
func (s *Switch) current(ctx context.Context) (stop Stop, version int64, found bool, err error) {
	err = s.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, Namespace, stopKey)
		switch {
		case core.HasCode(err, core.CodeNotFound):
			return nil
		case err != nil:
			return err
		}
		if err := json.Unmarshal(entry.Value, &stop); err != nil || stop.ID == "" {
			return core.NewError(core.CodeTampered, "the emergency stop record cannot be read")
		}
		version, found = entry.Version, true
		return nil
	})
	return stop, version, found, err
}

// Frozen reports whether publishing is frozen. A stop that cannot be read
// is reported with its error, and the caller treats it as frozen: the
// release pipeline holds when it cannot tell.
func (s *Switch) Frozen(ctx context.Context) (bool, error) {
	_, _, found, err := s.current(ctx)
	if err != nil {
		return true, err
	}
	return found, nil
}

// Status answers the supervisor's freeze status query.
func (s *Switch) Status(ctx context.Context) (supervisor.FreezeStatus, error) {
	stop, _, found, err := s.current(ctx)
	if err != nil {
		return supervisor.FreezeStatus{Frozen: true}, err
	}
	if !found {
		return supervisor.FreezeStatus{}, nil
	}
	return supervisor.FreezeStatus{Frozen: true, StopID: stop.ID, Actor: stop.Actor, Reason: stop.Reason, StoppedAt: stop.StoppedAt}, nil
}

// newStopID returns a random identifier of a stop.
func newStopID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", core.NewError(core.CodeInternal, "no randomness for the stop identifier").WithCause(err)
	}
	return "stop-" + hex.EncodeToString(b[:]), nil
}

// audit records a step of the switch. The stop does not depend on the log.
func (s *Switch) audit(ctx context.Context, action string, outcome core.AuditOutcome, details map[string]string) {
	if s.cfg.Audit == nil {
		return
	}
	_, _ = s.cfg.Audit.Append(ctx, core.AuditEvent{
		FactoryID: s.cfg.FactoryID, ActorID: actorID, ActorKind: core.ActorService,
		Action: action, Resource: "factory", Outcome: outcome, Details: details,
	})
}

package killswitch

import (
	"context"
	"fmt"

	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
)

// OperatorRequest is an emergency stop asked for from outside the factory,
// by the sisf CLI. Either route may be missing; the stop uses every one it
// is given.
type OperatorRequest struct {
	FactoryID core.FactoryID
	Actor     string
	Reason    string
	// StateDSN reaches the operational store directly; empty skips it.
	StateDSN core.SecretValue
	// AdminAddr is the supervisor's admin socket; empty skips it.
	AdminAddr string
	// AdminKeyID, when set, is the root key of the install being stopped:
	// the supervisor that answers must report a release signed by it, so a
	// stop is not sent to another install of the same factory id.
	AdminKeyID string
	Clock      core.Clock
}

// OperatorReport is what each route of an operator stop did.
type OperatorReport struct {
	// Store is the stop recorded straight in the store, when it was reached.
	Store *supervisor.EmergencyStopReport `json:"store,omitempty"`
	// Supervisor is the stop the supervisor carried out, when it answered.
	Supervisor *supervisor.EmergencyStopReport `json:"supervisor,omitempty"`
	// Failures names each route that could not be used.
	Failures []string `json:"failures,omitempty"`
}

// StopFromOperator stops the factory through the store and through the
// supervisor. The store is written first: it freezes publishing and holds
// every claim even when no supervisor answers. The supervisor then does the
// rest, because only it holds the agent runtime and the secret broker that
// cancel runs and revoke their secrets. Neither the control plane nor the
// dispatcher is involved.
//
// The report is returned in every case. The error is UNAVAILABLE when a
// route that was given could not be used, so the caller exits non-zero: a
// partial stop must not look complete.
func StopFromOperator(ctx context.Context, req OperatorRequest) (OperatorReport, error) {
	stop := supervisor.EmergencyStopRequest{FactoryID: req.FactoryID, Actor: req.Actor, Reason: req.Reason}
	if err := stop.Validate(); err != nil {
		return OperatorReport{}, err
	}
	if req.StateDSN.IsZero() && req.AdminAddr == "" {
		return OperatorReport{}, core.NewError(core.CodeInvalidArgument, "an emergency stop needs the state store, the supervisor socket or both")
	}
	var report OperatorReport
	if !req.StateDSN.IsZero() {
		if done, err := stopInStore(ctx, req, stop); err != nil {
			report.Failures = append(report.Failures, "store: "+err.Error())
		} else {
			report.Store = &done
		}
	}
	if req.AdminAddr != "" {
		if done, err := stopThroughSupervisor(ctx, req.AdminAddr, req.AdminKeyID, stop); err != nil {
			report.Failures = append(report.Failures, "supervisor: "+err.Error())
		} else {
			report.Supervisor = &done
		}
	}
	if len(report.Failures) > 0 {
		return report, core.NewError(core.CodeUnavailable, "the emergency stop could not use every route it was given")
	}
	return report, nil
}

// stopInStore records and applies the stop with the store alone.
func stopInStore(ctx context.Context, req OperatorRequest, stop supervisor.EmergencyStopRequest) (supervisor.EmergencyStopReport, error) {
	clock := req.Clock
	if clock == nil {
		clock = core.SystemClock{}
	}
	store, err := OpenControlStore(ctx, req.StateDSN, req.FactoryID, clock)
	if err != nil {
		return supervisor.EmergencyStopReport{}, err
	}
	defer func() { _ = store.Close() }()
	s, err := New(Config{FactoryID: req.FactoryID, State: store, Clock: clock})
	if err != nil {
		return supervisor.EmergencyStopReport{}, err
	}
	return s.Stop(ctx, stop)
}

// stopThroughSupervisor asks the supervisor to stop the factory, after it
// proved it serves that factory and, when keyID is set, this install. The
// admin address of Standalone mode is a fixed loopback port that another
// factory on the same host may hold; the supervisor refuses a stop for
// another factory too, but nothing is sent to one that is not this one.
func stopThroughSupervisor(ctx context.Context, addr, keyID string, stop supervisor.EmergencyStopRequest) (supervisor.EmergencyStopReport, error) {
	client, err := supervisor.NewClient(addr)
	if err != nil {
		return supervisor.EmergencyStopReport{}, err
	}
	if err := VerifySupervisor(ctx, client, stop.FactoryID, keyID); err != nil {
		return supervisor.EmergencyStopReport{}, err
	}
	return client.EmergencyStop(ctx, stop)
}

// VerifySupervisor proves that the supervisor client reaches serves factory
// and, when keyID is set, verified a release signed by that root key. A
// supervisor that is someone else's is CONFLICT, and its message says that
// nothing was sent to it.
func VerifySupervisor(ctx context.Context, client *supervisor.Client, factory core.FactoryID, keyID string) error {
	status, err := client.Status(ctx)
	if err != nil {
		return err
	}
	switch {
	case status.FactoryID != factory:
		return core.NewError(core.CodeConflict, fmt.Sprintf("the supervisor that answers serves factory %q, not %q; nothing was sent to it", status.FactoryID, factory))
	case keyID != "" && status.SelfCheck.ManifestKeyID != keyID:
		return core.NewError(core.CodeConflict, fmt.Sprintf("the supervisor that answers serves another install of factory %q (its release is signed by another root key); nothing was sent to it", factory))
	}
	return nil
}

// LiftFromOperator lifts the stop in force through the store. The supervisor
// socket cannot lift a stop, so lifting needs the store's credential.
func LiftFromOperator(ctx context.Context, factory core.FactoryID, dsn core.SecretValue, clock core.Clock, actor, reason string) error {
	if clock == nil {
		clock = core.SystemClock{}
	}
	store, err := OpenControlStore(ctx, dsn, factory, clock)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	s, err := New(Config{FactoryID: factory, State: store, Clock: clock})
	if err != nil {
		return err
	}
	return s.Lift(ctx, actor, reason)
}

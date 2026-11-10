package killswitch

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// StopAction and LiftAction are the audit actions of the switch.
const (
	StopAction = "factory.emergency_stop"
	LiftAction = "factory.emergency_stop_lift"
)

// leadershipResource is the lease the dispatcher leads under. Holding it
// keeps a dispatcher that is not leading from starting to.
const leadershipResource = "dispatcher"

// runNamespace and runKeyPrefix locate the dispatcher's run records, which
// the control zone may read.
const (
	runNamespace = "dispatcher"
	runKeyPrefix = "run/"
	runWorking   = "working"
)

// Stop stops the factory. The stop document is written first, so publishing
// is frozen even when every later part fails; the claims, the runs and the
// secrets are then stopped one by one, and each part that fails is listed
// in the report instead of undoing the others. A second stop keeps the
// first one's identity and applies it again.
func (s *Switch) Stop(ctx context.Context, req supervisor.EmergencyStopRequest) (supervisor.EmergencyStopReport, error) {
	if err := req.Validate(); err != nil {
		return supervisor.EmergencyStopReport{}, err
	}
	stop, err := s.record(ctx, req)
	if err != nil {
		s.audit(ctx, StopAction, core.OutcomeFailed, map[string]string{"requested_by": req.Actor})
		return supervisor.EmergencyStopReport{}, err
	}
	report := s.apply(ctx, stop)
	s.audit(ctx, StopAction, core.OutcomeExecuted, map[string]string{
		"stop_id": stop.ID, "requested_by": req.Actor, "reason": req.Reason,
		"claims_held": fmt.Sprint(len(report.ClaimsHeld)), "runs_cancelled": fmt.Sprint(len(report.RunsCancelled)),
		"failures": fmt.Sprint(len(report.Failures)),
	})
	return report, nil
}

// Enforce applies the stop in force again: it holds issues that arrived
// since, renews the holds and stops runs that started anyway. Without a
// stop it does nothing and reports nothing frozen.
func (s *Switch) Enforce(ctx context.Context) (supervisor.EmergencyStopReport, error) {
	stop, _, found, err := s.current(ctx)
	if err != nil || !found {
		return supervisor.EmergencyStopReport{}, err
	}
	return s.apply(ctx, stop), nil
}

// record writes the stop document, or returns the one already in force.
func (s *Switch) record(ctx context.Context, req supervisor.EmergencyStopRequest) (Stop, error) {
	var stop Stop
	err := s.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, Namespace, stopKey)
		switch {
		case err == nil:
			if json.Unmarshal(entry.Value, &stop) == nil && stop.ID != "" {
				return nil
			}
			// An unreadable record is replaced: a stop must always be possible.
		case !core.HasCode(err, core.CodeNotFound):
			return err
		}
		id, err := newStopID()
		if err != nil {
			return err
		}
		stop = Stop{ID: id, Actor: req.Actor, Reason: req.Reason, StoppedAt: s.cfg.Clock.Now().UTC()}
		raw, err := json.Marshal(stop)
		if err != nil {
			return core.NewError(core.CodeInternal, "the stop cannot be encoded").WithCause(err)
		}
		_, err = tx.PutKV(ctx, state.KVEntry{Namespace: Namespace, Key: stopKey, Value: raw, Version: entry.Version})
		return err
	})
	if err != nil {
		return Stop{}, core.NewError(core.CodeUnavailable, "the emergency stop could not be recorded").WithCause(err)
	}
	return stop, nil
}

// apply holds the claims and stops the runs of a recorded stop.
func (s *Switch) apply(ctx context.Context, stop Stop) supervisor.EmergencyStopReport {
	report := supervisor.EmergencyStopReport{StopID: stop.ID, StoppedAt: stop.StoppedAt, PublishingFrozen: true}
	records, runs, err := s.survey(ctx)
	if err != nil {
		report.Failures = append(report.Failures, "the open issues and runs could not be listed: "+string(core.CodeOf(err)))
	}
	s.holdClaims(ctx, records, &report)
	s.stopRuns(ctx, runs, &report)
	return report
}

// activeRun is a run the stop must end; agent is set when it has an agent
// run in flight.
type activeRun struct {
	id    core.RunID
	agent *dispatcherRun
}

// dispatcherRun is the part of a dispatcher run record the stop reads.
type dispatcherRun struct {
	RunID  core.RunID    `json:"run_id"`
	Status string        `json:"status"`
	Agent  *agent.RunRef `json:"agent,omitempty"`
}

// survey reads the open issues and the runs in flight in one snapshot.
func (s *Switch) survey(ctx context.Context) ([]core.StateRecord, []activeRun, error) {
	var records []core.StateRecord
	var runs []activeRun
	err := s.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		all, err := tx.ListRecords(ctx, state.RecordFilter{})
		if err != nil {
			return err
		}
		seen := map[core.RunID]int{}
		for _, r := range all {
			if r.State == core.StateClosed {
				continue
			}
			records = append(records, r)
			if statemachine.RequiresLease(r.State) && r.RunID != "" {
				seen[r.RunID] = len(runs)
				runs = append(runs, activeRun{id: r.RunID})
			}
		}
		entries, err := tx.ListKV(ctx, runNamespace, runKeyPrefix, 0)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			var run dispatcherRun
			if json.Unmarshal(entry.Value, &run) != nil || run.RunID.Validate() != nil || run.Status != runWorking {
				continue
			}
			if i, ok := seen[run.RunID]; ok {
				runs[i].agent = &run
				continue
			}
			seen[run.RunID] = len(runs)
			runs = append(runs, activeRun{id: run.RunID, agent: &run})
		}
		return nil
	})
	return records, runs, err
}

package killswitch

import (
	"context"
	"slices"
	"sort"

	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// rollbackStates are the states of an issue whose release is rolling out,
// watched or being undone. The stop does not hold their leases: the only
// way the supervisor moves them under a stop is back to the last known good
// release (the release pipeline checks the stop before every forward step),
// and its recovery needs the lease to write those steps. An emergency stop
// freezes forward progress; it never blocks a rollback.
var rollbackStates = []core.State{core.StateCanary, core.StateMonitoring, core.StateRolledBack}

// holdClaims takes the dispatcher's leadership and the lease of every open
// issue but those in rollbackStates, each in its own transaction so one
// refusal does not undo the others. An issue a live run still holds is not a failure: its run is
// cancelled, and a later pass takes the lease once the run lets it go. An
// unleased issue held by someone else is: something is claiming it.
func (s *Switch) holdClaims(ctx context.Context, records []core.StateRecord, report *supervisor.EmergencyStopReport) {
	// The leadership is a second line: without it a dispatcher cannot lead,
	// with it held by a live leader the issue leases still stop each claim.
	_ = s.hold(ctx, leadershipResource)
	for _, r := range records {
		if slices.Contains(rollbackStates, r.State) {
			continue
		}
		err := s.hold(ctx, state.IssueLeaseResource(r.IssueID))
		switch {
		case err == nil:
			report.ClaimsHeld = append(report.ClaimsHeld, r.IssueID)
		case core.HasCode(err, core.CodeLeaseHeld) && statemachine.RequiresLease(r.State):
		case core.HasCode(err, core.CodeLeaseHeld):
			report.Failures = append(report.Failures, "issue "+r.IssueID.String()+" is being claimed by another holder")
		default:
			report.Failures = append(report.Failures, "issue "+r.IssueID.String()+" could not be held: "+string(core.CodeOf(err)))
		}
	}
	sort.Slice(report.ClaimsHeld, func(i, j int) bool { return report.ClaimsHeld[i].String() < report.ClaimsHeld[j].String() })
}

// hold takes or renews the switch's lease on resource. Taking it again as
// the same holder is how the lease is renewed without its token.
func (s *Switch) hold(ctx context.Context, resource string) error {
	return s.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := tx.AcquireLease(ctx, resource, Holder, s.cfg.HoldTTL)
		return err
	})
}

// stopRuns cancels each agent run in flight and revokes every run's secret
// grants. Secrets are revoked even when the cancel fails: a run that cannot
// be stopped must at least lose its credentials.
func (s *Switch) stopRuns(ctx context.Context, runs []activeRun, report *supervisor.EmergencyStopReport) {
	if len(runs) == 0 {
		return
	}
	if s.cfg.Secrets == nil {
		report.Failures = append(report.Failures, "no secret broker is connected: the secret grants of the runs were not revoked")
	}
	cancellable := false
	for _, run := range runs {
		if run.agent != nil && run.agent.Agent != nil {
			cancellable = true
			if s.cfg.Runs != nil {
				if err := s.cfg.Runs.Cancel(ctx, *run.agent.Agent); err != nil && !core.HasCode(err, core.CodeNotFound) {
					report.Failures = append(report.Failures, "run "+string(run.id)+" could not be cancelled: "+string(core.CodeOf(err)))
				} else {
					report.RunsCancelled = append(report.RunsCancelled, run.id)
				}
			}
		}
		if s.cfg.Secrets != nil {
			if err := s.cfg.Secrets.RevokeRun(ctx, run.id); err != nil {
				report.Failures = append(report.Failures, "the secrets of run "+string(run.id)+" could not be revoked: "+string(core.CodeOf(err)))
			} else {
				report.SecretsRevoked = append(report.SecretsRevoked, run.id)
			}
		}
	}
	if cancellable && s.cfg.Runs == nil {
		report.Failures = append(report.Failures, "no agent runtime is connected: the runs in flight were not cancelled")
	}
}

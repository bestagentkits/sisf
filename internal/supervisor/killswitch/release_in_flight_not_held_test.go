package killswitch

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// inRelease writes issue n through a run's development, QA and release up
// to the release state left, the way the dispatcher and the supervisor
// write it, and lets every lease go, as a supervisor that stopped mid
// release left it once its lease lapsed.
func (f *factory) inRelease(n int, run core.RunID, left core.State) {
	f.t.Helper()
	record := f.ready(n)
	build := core.Digest("sha256:2222222222222222222222222222222222222222222222222222222222222222")
	err := f.control.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		lease, err := tx.AcquireLease(ctx, state.IssueLeaseResource(issue(n)), "supervisor", time.Hour)
		if err != nil {
			return err
		}
		steps := []struct {
			event core.Event
			to    core.State
		}{
			{core.EventClaimed, core.StateClaimed}, {core.EventDevelopmentStarted, core.StateDeveloping},
			{core.EventCandidateSubmitted, core.StateQAEvaluation}, {core.EventQAPassed, core.StateReleaseCandidate},
			{core.EventRolloutStarted, core.StateCanary}, {core.EventRolloutHealthy, core.StateMonitoring},
			{core.EventRegressionDetected, core.StateRolledBack},
		}
		for _, s := range steps {
			if record.State == left {
				break
			}
			t := core.Transition{IssueID: issue(n), From: record.State, To: s.to, Event: s.event, ExpectedVersion: record.Version,
				IdempotencyKey: "seed/" + string(run) + "/" + string(s.event), RunID: run, Attempt: 1,
				LeaseOwner: lease.Owner, FencingToken: lease.Token}
			switch s.to {
			case core.StateQAEvaluation, core.StateReleaseCandidate:
				t.CandidateDigest = build
			case core.StateCanary:
				t.CandidateDigest, t.ReleaseDigest = build, core.ReleaseDigest(build)
			case core.StateMonitoring:
				t.ReleaseDigest = core.ReleaseDigest(build)
			}
			if record, err = tx.Transition(ctx, t); err != nil {
				return err
			}
		}
		return tx.ReleaseLease(ctx, lease)
	})
	if err != nil {
		f.t.Fatalf("issue %d to %s: %v", n, left, err)
	}
}

// An emergency stop freezes forward progress but never blocks a rollback:
// the issues whose release is rolling out, watched or being undone are not
// held, so the supervisor can still restore the last known good release and
// write the steps back.
func TestAnEmergencyStopLeavesReleasesInFlightToTheSupervisorsRecovery(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.ready(1)
	for i, left := range []core.State{core.StateCanary, core.StateMonitoring, core.StateRolledBack} {
		f.inRelease(2+i, core.RunID(fmt.Sprintf("run-%d", 2+i)), left)
	}
	f.inRelease(5, "run-candidate", core.StateReleaseCandidate)
	report, err := f.kill().Stop(context.Background(), stopRequest())
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !slices.Equal(report.ClaimsHeld, []core.IssueRef{issue(1), issue(5)}) {
		t.Fatalf("claims held %v, want the Ready issue and the release candidate only", report.ClaimsHeld)
	}
	for n := 2; n <= 4; n++ {
		err := f.control.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
			_, err := tx.AcquireLease(ctx, state.IssueLeaseResource(issue(n)), "supervisor", time.Minute)
			return err
		})
		if err != nil {
			t.Fatalf("the supervisor cannot take issue %d during the stop: %v", n, err)
		}
	}
}

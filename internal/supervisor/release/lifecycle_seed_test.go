package release

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// issueRef returns the reference of issue number n of the sample repository.
func issueRef(n int) core.IssueRef { return core.IssueRef{Owner: "acme", Repo: "hello", Number: n} }

// seedReleaseCandidate writes the lifecycle of issue up to ReleaseCandidate
// for run, with build as the candidate that passed QA, the way the control
// plane and the dispatcher write it, and lets the run's lease go so the
// supervisor can take the issue.
func (w *world) seedReleaseCandidate(issue core.IssueRef, run core.RunID, build core.Digest) {
	w.t.Helper()
	ctx := context.Background()
	err := w.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		record, err := tx.Transition(ctx, core.Transition{
			IssueID: issue, To: core.StateIntake, Event: core.EventIssueReceived, IdempotencyKey: "intake/" + issue.String() + "/received",
		})
		if err != nil {
			return err
		}
		next := func(event core.Event, to core.State, change func(*core.Transition)) error {
			t := core.Transition{
				IssueID: issue, From: record.State, To: to, Event: event, ExpectedVersion: record.Version,
				IdempotencyKey: "seed/" + issue.String() + "/" + string(event), RunID: record.RunID, Attempt: record.Attempt,
				LeaseOwner: record.LeaseOwner, FencingToken: record.FencingToken,
			}
			if change != nil {
				change(&t)
			}
			record, err = tx.Transition(ctx, t)
			return err
		}
		if err := next(core.EventInfoSufficient, core.StateScopeDraft, nil); err != nil {
			return err
		}
		if err := next(core.EventScopePermitted, core.StateReady, nil); err != nil {
			return err
		}
		lease, err := tx.AcquireLease(ctx, state.IssueLeaseResource(issue), "dispatcher-1", time.Hour)
		if err != nil {
			return err
		}
		steps := []struct {
			event  core.Event
			to     core.State
			change func(*core.Transition)
		}{
			{core.EventClaimed, core.StateClaimed, func(t *core.Transition) {
				t.LeaseOwner, t.FencingToken, t.RunID, t.Attempt = lease.Owner, lease.Token, run, 1
			}},
			{core.EventDevelopmentStarted, core.StateDeveloping, nil},
			{core.EventCandidateSubmitted, core.StateQAEvaluation, func(t *core.Transition) { t.CandidateDigest = build }},
			{core.EventQAPassed, core.StateReleaseCandidate, func(t *core.Transition) { t.CandidateDigest = build }},
		}
		for _, s := range steps {
			if err := next(s.event, s.to, s.change); err != nil {
				return err
			}
		}
		return tx.ReleaseLease(ctx, lease)
	})
	if err != nil {
		w.t.Fatalf("seed %s to ReleaseCandidate: %v", issue, err)
	}
}

// requireState fails the test unless issue is stored in want.
func (w *world) requireState(issue core.IssueRef, want core.State) {
	w.t.Helper()
	if got := w.record(issue).State; got != want {
		w.t.Fatalf("issue %s is in %s, want %s", issue, got, want)
	}
}

// holdLease has owner take the issue lease, as a run that still works on
// the issue would hold it.
func (w *world) holdLease(issue core.IssueRef, owner string) {
	w.t.Helper()
	err := w.store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		_, err := tx.AcquireLease(ctx, state.IssueLeaseResource(issue), owner, time.Hour)
		return err
	})
	if err != nil {
		w.t.Fatalf("hold the lease of %s: %v", issue, err)
	}
}

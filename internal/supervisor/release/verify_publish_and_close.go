package release

import (
	"context"
	"strconv"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// finish closes a release that passed its observation window. The revision
// is read from the running system once more after the window: only the
// release itself, healthy, is verified. The verified release is recorded
// as the LKG first, so that a supervisor that stops at any later point
// starts this release again, as the record will say. The Verified step is
// written next, with what the release still owes (FinishDue) in the same
// transaction; then the changelog and the catalog are published, the change
// is landed and the issue closes (complete). A step after the Verified step
// that cannot be taken leaves the issue Verified with its finish due, which
// FinishVerified takes up on a later pass.
func (p *Pipeline) finish(ctx context.Context, r *rollout) (Outcome, error) {
	out := Outcome{Release: r.rev.Digest, Windows: r.windows, State: r.h.record.State}
	windowEnd := p.cfg.Clock.Now()
	observed, err := p.cfg.Backend.ObservedRevision(ctx, r.unit)
	if err != nil {
		return p.restore(ctx, r, *unjudged("the release cannot be observed after its observation window"))
	}
	r.observed, out.Observed = observed, observed
	if !observed.Matches(r.rev) {
		return p.restore(ctx, r, *judgedUnhealthy("regression: the revision observed after the window is not the healthy release"))
	}
	dod := []statemachine.DoDResult{
		{Item: "the release is observed running and healthy", Met: true, EvidenceRef: "observed:" + string(observed.Digest)},
		{Item: "no rollback rule fired through the observation window", Met: true, EvidenceRef: "windows:" + strconv.Itoa(len(r.windows))},
	}
	if r.lowTraffic {
		dod = append(dod, statemachine.DoDResult{Item: "every synthetic check passed through the low-traffic hold", Met: true})
	}
	proof := statemachine.PostDeployProof{
		IssueID: r.h.record.IssueID, RunID: r.h.record.RunID, Released: r.rev.Digest, Observed: observed.Digest,
		Healthy: observed.Healthy, ObservedAt: observed.ObservedAt, WindowEndsAt: windowEnd, DoD: dod,
	}
	// A failed write of the LKG changes nothing on the trust volume; the
	// release is still verified, and the LKG is recorded on a later pass
	// (FinishVerified) or before the LKG is adopted at the next start.
	lkgErr := p.cfg.LKG.record(r.v)
	due := FinishDue{IssueID: r.h.record.IssueID, RunID: r.h.record.RunID, Release: r.rev.Digest, Unit: r.unit}
	err = p.step(ctx, r.h, stepSpec{
		event: core.EventPostDeployVerified, reason: "the observed release met its DoD through the observation window",
		release: r.rev.Digest, evidence: []string{"observed:" + string(observed.Digest)},
		guards: statemachine.Guards{PostDeploy: statemachine.PostDeployGuardFunc(func(context.Context, core.Transition) (statemachine.PostDeployProof, error) {
			return proof, nil
		})},
		also: func(ctx context.Context, tx state.Tx, _ core.StateRecord) error { return putFinishDue(ctx, tx, due) },
	})
	switch {
	case err != nil && lkgErr == nil:
		// The release is the LKG already, so restoring the LKG would
		// change nothing; the recovery verifies it from the running system
		// on the next pass instead.
		out.Result, out.Reason, out.State = ResultInterrupted, "the verified release could not be recorded: "+err.Error(), r.h.record.State
		return out, err
	case err != nil:
		// The release served through its window but neither its LKG nor its
		// verification could be recorded, so it cannot be kept unwatched.
		return p.restore(ctx, r, undo{cause: statemachine.BlockReleaseInterrupted,
			reason: "the verified release could not be recorded, so it could not be kept: " + err.Error()})
	case lkgErr != nil:
		out.Result, out.Reason, out.State, out.Waits = ResultHeld, lkgErr.Error(), r.h.record.State, waits(waitsLKG, lkgErr)
		return out, lkgErr
	}
	done, err := p.complete(ctx, &finishing{h: r.h, due: due, feature: r.feature, summary: r.summary, candidate: r.candidate,
		receipt: r.v.receipt, evidence: []string{"observed:" + string(observed.Digest)}})
	done.Windows, done.Observed = r.windows, observed
	return done, err
}

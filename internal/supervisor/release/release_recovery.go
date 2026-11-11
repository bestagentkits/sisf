package release

import (
	"context"
	"slices"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/state"
)

// RecoverableStates are the states of a release whose rollout started and
// that has not been blocked or verified yet. A record in one of them that
// no release of this process drives was left by a supervisor that stopped,
// or by a release that could not finish, and Recover undoes it.
func RecoverableStates() []core.State {
	return []core.State{core.StateCanary, core.StateMonitoring, core.StateRolledBack}
}

// interruptedReason is the reason recorded for a release Recover undid.
const interruptedReason = "the release was no longer watched before it was judged, because the supervisor stopped or could not go on with it, so the last known good release was restored"

// Recover undoes the release of issue when it is left in Canary, Monitoring
// or RolledBack with nothing of this pipeline watching it: it restores the
// LKG of unit through the deployment backend, writes RolledBack and then
// Blocked with the recovery report, and records that the release was
// interrupted. A release that was undone earlier but whose block could not
// be written is finished the same way.
//
// It is safe to call at any time and more than once: an issue a release or
// another recovery of this pipeline drives is left alone, the issue lease
// is taken before anything is switched, and an issue that is no longer in
// one of those states yields an Outcome without a Result.
func (p *Pipeline) Recover(ctx context.Context, issue core.IssueRef, unit string) (Outcome, error) {
	if err := issue.Validate(); err != nil {
		return Outcome{}, err
	}
	done, ok := p.drive(issue)
	if !ok {
		return Outcome{}, nil
	}
	defer done()
	h, err := p.take(ctx, issue)
	if err != nil {
		return Outcome{}, err
	}
	defer p.letGo(ctx, h)
	out := Outcome{State: h.record.State, Release: h.record.ReleaseDigest}
	if !slices.Contains(RecoverableStates(), h.record.State) {
		return out, nil
	}
	failed := func(err error) (Outcome, error) {
		out.Result, out.Reason = ResultRestoreFailed, err.Error()
		return out, err
	}
	if h.record.ReleaseDigest.Validate() != nil {
		return failed(core.NewError(core.CodeTampered, "the record of a release in "+string(h.record.State)+" names no release"))
	}
	lkg, found, err := p.cfg.LKG.Get(ctx, unit)
	switch {
	case err != nil:
		return failed(holdf("the last known good release of %s cannot be read", unit).WithCause(err))
	case !found:
		return failed(holdf("unit %s has no last known good release to restore", unit))
	case lkg.Digest == h.record.ReleaseDigest && h.record.State == core.StateMonitoring &&
		lkg.Source == SourcePromoted && lkg.RunID == h.record.RunID:
		return p.verifyRecordedLKG(ctx, h, unit, lkg)
	case lkg.Digest == h.record.ReleaseDigest:
		return failed(holdf("the release left in %s is already the last known good release of %s, so there is nothing to restore",
			h.record.State, unit))
	}
	r := &rollout{
		h: h, unit: unit, lkg: lkg,
		rev:    deploy.Revision{Unit: unit, Digest: h.record.ReleaseDigest},
		lkgRev: deploy.Revision{Unit: unit, Digest: lkg.Digest},
	}
	return p.restore(ctx, r, undo{cause: statemachine.BlockReleaseInterrupted, reason: interruptedReason})
}

// verifyRecordedLKG writes the Verified step of a release the pipeline
// recorded as the LKG after its observation window, but whose Verified step
// was never written because the supervisor stopped or the write failed in
// between. Only the pipeline's finish records an LKG, and only after the
// window, so the entry is the record that the release passed it; the
// running system is read once more, and the step is written only when it
// serves the release, healthy. What the release still owes is recorded as
// due with the step, and FinishVerified takes it up. Restoring the LKG
// would change nothing: the release is the LKG.
func (p *Pipeline) verifyRecordedLKG(ctx context.Context, h *holding, unit string, lkg LKG) (Outcome, error) {
	out := Outcome{State: h.record.State, Release: h.record.ReleaseDigest}
	rev := deploy.Revision{Unit: unit, Digest: h.record.ReleaseDigest}
	observed, err := p.cfg.Backend.ObservedRevision(ctx, unit)
	if err != nil || !observed.Matches(rev) {
		out.Result = ResultRestoreFailed
		err = holdf("the release recorded as the last known good release of %s is not observed running and healthy", unit)
		out.Reason = err.Error()
		return out, err
	}
	out.Observed = observed
	proof := statemachine.PostDeployProof{
		IssueID: h.record.IssueID, RunID: h.record.RunID, Released: rev.Digest, Observed: observed.Digest,
		Healthy: observed.Healthy, ObservedAt: observed.ObservedAt, WindowEndsAt: lkg.RecordedAt,
		DoD: []statemachine.DoDResult{
			{Item: "the release was recorded as the last known good release after its observation window", Met: true, EvidenceRef: "lkg:" + string(lkg.Digest)},
			{Item: "the release is observed running and healthy", Met: true, EvidenceRef: "observed:" + string(observed.Digest)},
		},
	}
	due := FinishDue{IssueID: h.record.IssueID, RunID: h.record.RunID, Release: rev.Digest, Unit: unit}
	err = p.step(ctx, h, stepSpec{
		event: core.EventPostDeployVerified, reason: "the release was recorded as the LKG after its observation window and is observed serving",
		release: rev.Digest, evidence: []string{"observed:" + string(observed.Digest), "lkg:" + string(lkg.Digest)},
		guards: statemachine.Guards{PostDeploy: statemachine.PostDeployGuardFunc(func(context.Context, core.Transition) (statemachine.PostDeployProof, error) {
			return proof, nil
		})},
		also: func(ctx context.Context, tx state.Tx, _ core.StateRecord) error { return putFinishDue(ctx, tx, due) },
	})
	out.State = h.record.State
	return out, err
}

// keepLease makes sure the pipeline still holds the issue lease it is about
// to act under, before it switches anything. A lease that lapsed with no
// successor is taken again, if the record is still as the pipeline last
// wrote it; a lease another holder took is not, and the caller stops.
func (p *Pipeline) keepLease(ctx context.Context, h *holding) error {
	err := p.renew(ctx, h)
	if !core.HasCode(err, core.CodeExpired) {
		return err
	}
	return p.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		lease, err := tx.AcquireLease(ctx, h.lease.Resource, p.cfg.LeaseOwner, p.cfg.LeaseTTL)
		if err != nil {
			return err
		}
		record, err := tx.GetRecord(ctx, h.record.IssueID)
		if err != nil {
			return err
		}
		if record.Version != h.record.Version {
			return core.NewError(core.CodeConflict, "the issue changed while its lease had lapsed")
		}
		h.lease, h.record = lease, record
		return nil
	})
}

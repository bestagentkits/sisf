package release

import (
	"context"
	"errors"
	"slices"
	"strconv"

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
	if core.HasCode(err, core.CodeLeaseHeld) {
		return p.restoreWithoutLease(ctx, issue, unit, err)
	}
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
	why := undo{cause: statemachine.BlockReleaseInterrupted, reason: interruptedReason}
	if h.record.State == core.StateRolledBack {
		// A restore that rolled the release back but could not block the
		// issue kept why it did; the block says the same.
		kept, found, err := p.readRestoreCause(ctx, h.record.RunID)
		if err != nil {
			return failed(err)
		}
		if found {
			why = kept
		}
	}
	return p.restore(ctx, r, why)
}

// restoreWithoutLease rolls the unit back to its LKG when the release of
// issue is left unwatched but another holder keeps its lease: an emergency
// stop that held it, or a role that took it over. Rolling back is always
// the safe direction, so it does not wait for the lease; only the steps
// do, and they are written by a later recovery that can take it. The LKG is
// switched to only when it is not observed serving already, so a recovery
// that is retried on every pass switches it once. The outcome says on whom
// the steps wait (Waits) and carries LEASE_HELD.
//
// The LKG is switched to once per release and process: a canary share is
// not seen in what the running system reports, so the switch is not
// skipped on an observation, and the recovery retried on every pass does
// not switch again.
func (p *Pipeline) restoreWithoutLease(ctx context.Context, issue core.IssueRef, unit string, held error) (Outcome, error) {
	var record core.StateRecord
	if err := p.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		record, err = tx.GetRecord(ctx, issue)
		return err
	}); err != nil {
		return Outcome{}, err
	}
	out := Outcome{State: record.State, Release: record.ReleaseDigest}
	if !slices.Contains(RecoverableStates(), record.State) || record.ReleaseDigest.Validate() != nil {
		return out, held
	}
	holder := "an unknown holder"
	var refused *core.Error
	if errors.As(held, &refused) && refused.Details["holder"] != "" {
		holder = strconv.Quote(refused.Details["holder"])
	}
	failed := func(err error) (Outcome, error) {
		out.Result, out.Reason = ResultRestoreFailed, err.Error()
		out.Waits = "the issue lease is held by " + holder + ", and the last known good release could not be restored: " + string(core.CodeOf(err))
		return out, err
	}
	lkg, found, err := p.cfg.LKG.Get(ctx, unit)
	switch {
	case err != nil:
		return failed(holdf("the last known good release of %s cannot be read", unit).WithCause(err))
	case !found:
		return failed(holdf("unit %s has no last known good release to restore", unit))
	case lkg.Digest == record.ReleaseDigest:
		out.Waits = "the issue lease is held by " + holder + "; the release is the last known good release, so nothing is restored"
		return out, held
	}
	target := deploy.Revision{Unit: unit, Digest: lkg.Digest}
	if !p.restoredOnce(issue, record.ReleaseDigest, lkg.Digest) {
		r := &rollout{h: &holding{record: record}, unit: unit, rev: deploy.Revision{Unit: unit, Digest: record.ReleaseDigest}}
		charge := p.runRollbackCharge(r)
		spend := p.reserveRollback(ctx, charge)
		err := p.cfg.Backend.Rollback(ctx, target)
		p.recordRollbackSpend(ctx, charge, spend)
		if err != nil {
			return failed(holdf("the last known good release could not be restored").WithCause(err))
		}
		observed, err := p.cfg.Backend.ObservedRevision(ctx, unit)
		if err != nil || !observed.Matches(target) {
			return failed(holdf("the last known good release is not observed running and healthy after the restore"))
		}
		out.Observed = observed
		p.markRestored(issue, record.ReleaseDigest, lkg.Digest)
	}
	out.Result, out.RestoredTo, out.Reason = ResultInterrupted, lkg.Digest, held.Error()
	out.Waits = "the issue lease is held by " + holder + ", so the release's steps wait; the last known good release serves again"
	return out, held
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

// restoredOnce reports whether this pipeline already restored to to the
// unit released as release of issue without the issue lease.
func (p *Pipeline) restoredOnce(issue core.IssueRef, release, to core.ReleaseDigest) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.restoredWithoutLease[issue] == string(release)+">"+string(to)
}

// markRestored remembers a restore restoreWithoutLease made.
func (p *Pipeline) markRestored(issue core.IssueRef, release, to core.ReleaseDigest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.restoredWithoutLease[issue] = string(release) + ">" + string(to)
}

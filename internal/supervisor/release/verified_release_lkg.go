package release

import (
	"context"
	"time"

	iqa "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/internal/supplychain"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/state"
)

// The one invariant of the last known good release (LKG) is that it names
// what serves the unit: the LKG is recorded only for a release observed
// serving the unit, healthy, at that moment. A verified release is recorded
// after its Verified step, so a release whose step could not be written is
// still undone to the LKG before it. Releases of a unit are ordered by
// their Verified step, never by when an LKG entry was written. A release a
// later one superseded does not take the LKG back; one that is neither
// serving, healthy, nor superseded cannot be finished and blocks its issue.

// lkgVerdict is what settleLKG decided for a verified release.
type lkgVerdict string

const (
	// lkgNamesIt: the LKG names the release already.
	lkgNamesIt lkgVerdict = "names_it"
	// lkgRecorded: the release serves, healthy, and was recorded as the LKG.
	lkgRecorded lkgVerdict = "recorded"
	// lkgSuperseded: a release verified later, or a later install, holds
	// the LKG or was verified after it; the release is finished without it.
	lkgSuperseded lkgVerdict = "superseded"
	// lkgUnjudged: the running system answers that the release is not what
	// serves, or not healthy, or its QA evidence does not verify even as of
	// its verification, and nothing superseded it: it cannot be finished.
	lkgUnjudged lkgVerdict = "unjudged"
)

// lkgDecision is the verdict on a verified release and what it was based on.
type lkgDecision struct {
	verdict lkgVerdict
	// reason says why a release is superseded or unjudged, in fixed words
	// with digests and run names only.
	reason string
	// lkg and found are the LKG of the unit as read for the decision.
	lkg   LKG
	found bool
	// observed is what the running system answered.
	observed deploy.Observed
}

// settleLKG decides, and when it may records, the LKG for the verified
// release of record. It writes no lifecycle step, so it also runs at
// startup before the LKG is adopted. An error decides nothing: the LKG or
// the store could not be read, the artifact could not be verified now or the
// LKG could not be written; the release waits and is tried again.
func (p *Pipeline) settleLKG(ctx context.Context, record core.StateRecord, req Request) (lkgDecision, error) {
	unit := req.Artifact.Unit
	var d lkgDecision
	var err error
	d.lkg, d.found, err = p.cfg.LKG.Get(ctx, unit)
	if err != nil {
		return d, err
	}
	if d.found && d.lkg.Source == SourcePromoted && d.lkg.Digest == record.ReleaseDigest && d.lkg.RunID == record.RunID {
		d.verdict = lkgNamesIt
		return d, nil
	}
	superseded, err := p.supersededBy(ctx, record, unit, d.lkg, d.found)
	if err != nil {
		return d, err
	}
	if superseded != "" {
		d.verdict, d.reason = lkgSuperseded, superseded
		return d, nil
	}
	var observeErr error
	d.observed, observeErr = p.cfg.Backend.ObservedRevision(ctx, unit)
	switch {
	case observeErr != nil:
		// A running system that does not answer is an outage that clears,
		// like the store's: the release waits and is judged once it answers.
		return d, core.NewError(core.CodeUnavailable, "the running system of the unit cannot be observed").WithCause(observeErr)
	case !d.observed.Matches(deploy.Revision{Unit: unit, Digest: record.ReleaseDigest}):
		d.verdict, d.reason = lkgUnjudged, "the verified release is no longer observed serving its unit, healthy ("+
			string(d.observed.Digest)+" serves), and no later release replaced it"
		return d, nil
	}
	v, err := p.verifyAsOf(ctx, req, record, record.UpdatedAt)
	if err != nil {
		if ctx.Err() == nil && evidenceJudged(err) {
			d.verdict, d.reason = lkgUnjudged, "the QA evidence of the verified release does not verify even as of its verification ("+string(core.CodeOf(err))+")"
			return d, nil
		}
		return d, err
	}
	v.verifiedStepAt = record.UpdatedAt
	if err := p.cfg.LKG.record(v); err != nil {
		return d, err
	}
	d.verdict = lkgRecorded
	return d, nil
}

// supersededBy says which release superseded the verified release of
// record, "" when none did. A release is superseded when the LKG names
// another release verified after it, when a later install pins the LKG,
// or when another release of the unit was verified after it and still
// waits to be finished. Releases are ordered by their Verified step; equal
// instants supersede nothing.
func (p *Pipeline) supersededBy(ctx context.Context, record core.StateRecord, unit string, lkg LKG, found bool) (string, error) {
	verifiedAt := record.UpdatedAt
	if found {
		switch {
		case lkg.Source == SourcePromoted && lkg.RunID != record.RunID && lkg.verifiedStep().After(verifiedAt):
			return "the release of " + string(lkg.RunID) + " (" + string(lkg.Digest) + "), verified later, is the last known good release", nil
		case lkg.Source == SourceReleaseManifest && lkg.RecordedAt.After(verifiedAt):
			return "a later install pins " + string(lkg.Digest) + " as the last known good release", nil
		}
	}
	later := ""
	err := p.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		records, err := tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateVerified}})
		if err != nil {
			return err
		}
		for _, r := range records {
			if r.IssueID == record.IssueID || !r.UpdatedAt.After(verifiedAt) {
				continue
			}
			due, err := ReadFinishDue(ctx, tx, r.RunID)
			if core.HasCode(err, core.CodeNotFound) {
				continue // an earlier build's record names no unit
			}
			if err != nil {
				return err
			}
			if due.Unit == unit && due.Release == r.ReleaseDigest {
				later = "the release of " + string(r.RunID) + " (" + string(r.ReleaseDigest) + ") was verified later"
				return nil
			}
		}
		return nil
	})
	return later, err
}

// verifyAsOf verifies the release request of an already verified record:
// its QA receipt as of at, the instant of its Verified step, with a
// verifier that judges the delegated keys as of then, so that a receipt or
// key that expired since does not hold the release for good; and its
// artifact, which the supervisor signs again on every pass with the key it
// holds, as of now.
func (p *Pipeline) verifyAsOf(ctx context.Context, req Request, record core.StateRecord, at time.Time) (verifiedRelease, error) {
	if record.CandidateDigest != req.Candidate.Digest {
		return verifiedRelease{}, core.NewError(core.CodeGuardFailed, "the candidate is not the one the issue's run submitted")
	}
	verifier := p.cfg.Verifier
	if p.cfg.VerifierAt != nil {
		pinned, err := p.cfg.VerifierAt(at)
		if err != nil {
			return verifiedRelease{}, core.NewError(core.CodeUnavailable, "the keys trusted at the verification cannot be read").WithCause(err)
		}
		verifier = pinned
	}
	if err := iqa.VerifyReceipt(ctx, verifier, req.Receipt, p.cfg.FactoryID, req.Candidate, at); err != nil {
		return verifiedRelease{}, err
	}
	if err := req.Receipt.CheckPass(p.cfg.PassScore, p.cfg.ExtraGates...); err != nil {
		return verifiedRelease{}, err
	}
	if req.Artifact.Digest != core.ReleaseDigest(req.Candidate.Digest) || req.Artifact.Digest != record.ReleaseDigest {
		return verifiedRelease{}, core.NewError(core.CodeGuardFailed, "the artifact is another build than the release the record names")
	}
	if err := supplychain.VerifyArtifact(ctx, p.cfg.Verifier, req.Artifact, p.cfg.FactoryID, req.Artifact.Digest); err != nil {
		// The supervisor's own signature, made now: a key it cannot use
		// now is put right by the Owner, so the release waits for it.
		return verifiedRelease{}, core.NewError(core.CodeReleaseHold, "the supervisor's signature of the build does not verify now").WithCause(err)
	}
	return verifiedRelease{issue: req.IssueID, run: req.RunID, artifact: req.Artifact, receipt: req.Receipt, verifiedAt: p.cfg.Clock.Now()}, nil
}

// evidenceJudged reports whether a verification error judged the QA
// evidence itself, so that trying again cannot change it: anything but a
// verifier, key or signature of the supervisor's own that could not be had
// now.
func evidenceJudged(err error) bool {
	switch core.CodeOf(err) {
	case core.CodeUnavailable, core.CodeReleaseHold, core.CodeInternal:
		return false
	}
	return true
}

// blockVerified blocks the issue of a verified release that cannot be
// finished (lkgUnjudged). When the release may still be what serves, and
// another release is the LKG, the unit is first switched back to it, so no
// release that cannot be judged keeps serving; when there is nothing to
// switch back to, the block says so. What the release owed is dropped with
// the step: it is not published and its pull request is left open and
// unmerged for the Owner. A switch that does not take leaves the issue
// Verified, and it is tried again.
func (p *Pipeline) blockVerified(ctx context.Context, h *holding, unit string, d lkgDecision) (Outcome, error) {
	release := h.record.ReleaseDigest
	out := Outcome{Release: release, State: h.record.State, Observed: d.observed, Reason: d.reason}
	r := &rollout{h: h, unit: unit, rev: deploy.Revision{Unit: unit, Digest: release}, observed: d.observed}
	mayServe := d.observed.Digest == release
	reason := d.reason
	switch {
	case mayServe && d.found && d.lkg.Digest != release:
		r.lkg, r.lkgRev = d.lkg, deploy.Revision{Unit: unit, Digest: d.lkg.Digest}
		observed, result, err := p.switchBack(ctx, r)
		if err != nil {
			out.Result, out.Reason = result, err.Error()
			out.Waits = "the verified release cannot be judged and the last known good release could not be restored yet (" +
				string(core.CodeOf(err)) + "); this is tried again on every pass"
			_ = p.audit(ctx, h, "release.restore", core.OutcomeFailed, map[string]string{"reason": d.reason})
			return out, err
		}
		out.Observed, out.RestoredTo = observed, observed.Digest
		reason += "; the last known good release was restored"
	case mayServe:
		reason += "; there is no other release to restore it to, so nothing was restored"
	default:
		reason += "; nothing was switched back"
	}
	now := p.cfg.Clock.Now().UTC()
	report := RecoveryReport{
		IssueID: h.record.IssueID, RunID: h.record.RunID, Release: release, RestoredTo: out.RestoredTo, Reason: reason,
		Cause: statemachine.BlockReleaseUnjudged, Observed: out.Observed, DataUntouched: true, At: now,
	}
	block := Block{
		IssueID: h.record.IssueID, RunID: h.record.RunID, Attempt: h.record.Attempt, Cause: statemachine.BlockReleaseUnjudged,
		Release: release, RestoredTo: out.RestoredTo, Verified: true, BlockedAt: now,
	}
	err := p.step(ctx, h, stepSpec{
		event: core.EventVerifiedReleaseUnjudged, reason: reason,
		evidence: []string{"observed:" + string(out.Observed.Digest), "kv:" + RecoveryNamespace + "/" + RecoveryKey(h.record.RunID)},
		also: func(ctx context.Context, tx state.Tx, rec core.StateRecord) error {
			if err := writeRecoveryReport(ctx, tx, report); err != nil {
				return err
			}
			if err := deleteFinishDue(ctx, tx, rec.RunID); err != nil {
				return err
			}
			if err := deleteLandingDue(ctx, tx, rec.RunID); err != nil {
				return err
			}
			return p.cfg.Blocks.RecordBlock(ctx, tx, block)
		},
	})
	out.State, out.Reason = h.record.State, reason
	if err != nil {
		out.Result = ResultInterrupted
		out.Waits = waits(waitsClose, err)
		return out, err
	}
	out.Result, out.Cause = ResultBlocked, statemachine.BlockReleaseUnjudged
	return out, p.audit(ctx, h, "release.unjudged", core.OutcomeExecuted, map[string]string{
		"release": string(release), "restored_to": string(out.RestoredTo), "reason": reason,
	})
}

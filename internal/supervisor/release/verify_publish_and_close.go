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
// release itself, healthy, is verified. The Verified step is written first,
// with what the release still owes (FinishDue) in the same transaction, and
// the LKG after it (finishVerified): a step that cannot be written leaves
// the LKG naming the release before this one, so the release is restored to
// it. A supervisor that stops between the step and the LKG records the LKG
// at its next start or on a later pass (RecordVerifiedLKG, FinishVerified).
// Then the changelog and the catalog are published, the change is landed
// and the issue closes (complete). A step after the Verified step that
// cannot be taken leaves the issue Verified with its finish due, which
// FinishVerified takes up on a later pass.
func (p *Pipeline) finish(ctx context.Context, r *rollout) (Outcome, error) {
	windowEnd := p.cfg.Clock.Now()
	observed, err := p.cfg.Backend.ObservedRevision(ctx, r.unit)
	if err != nil {
		return p.restore(ctx, r, *unjudged("the release cannot be observed after its observation window"))
	}
	r.observed = observed
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
	due, err := p.verifiedDue(ctx, FinishDue{IssueID: r.h.record.IssueID, RunID: r.h.record.RunID, Release: r.rev.Digest, Unit: r.unit})
	if err != nil {
		return p.restore(ctx, r, undo{cause: statemachine.BlockReleaseInterrupted,
			reason: "the install in force could not be read, so the verified release could not be recorded: " + err.Error()})
	}
	err = p.step(ctx, r.h, stepSpec{
		event: core.EventPostDeployVerified, reason: "the observed release met its DoD through the observation window",
		release: r.rev.Digest, evidence: []string{"observed:" + string(observed.Digest)},
		guards: statemachine.Guards{PostDeploy: statemachine.PostDeployGuardFunc(func(context.Context, core.Transition) (statemachine.PostDeployProof, error) {
			return proof, nil
		})},
		also: func(ctx context.Context, tx state.Tx, _ core.StateRecord) error { return putVerifiedDue(ctx, tx, &due) },
	})
	if err != nil {
		// The LKG still names the release before this one, so the release
		// served through its window but cannot be kept unwatched.
		return p.restore(ctx, r, undo{cause: statemachine.BlockReleaseInterrupted,
			reason: "the verified release could not be recorded, so it could not be kept: " + err.Error()})
	}
	req := Request{IssueID: r.h.record.IssueID, RunID: r.h.record.RunID, Candidate: r.candidate, Receipt: r.v.receipt,
		Artifact: r.v.artifact, Feature: r.feature, Summary: r.summary}
	done, err := p.finishVerified(ctx, r.h, req, due, []string{"observed:" + string(observed.Digest)})
	done.Windows = r.windows
	if done.Observed.Digest == "" {
		done.Observed = observed
	}
	return done, err
}

// finishVerified settles the LKG of a verified release and finishes it:
// recorded or already the LKG, it is published, landed and closed; a
// release a later one superseded is closed without the LKG, unpublished, its
// pull request left for the Owner unless it was merged already; one that
// cannot be judged is blocked (blockVerified). When the LKG cannot be
// settled now, the issue stays Verified and says what it waits on.
func (p *Pipeline) finishVerified(ctx context.Context, h *holding, req Request, due FinishDue, evidence []string) (Outcome, error) {
	d, err := p.settleLKG(ctx, h.record, req, due)
	if err != nil {
		out := Outcome{Release: h.record.ReleaseDigest, State: h.record.State, Result: ResultHeld, Reason: err.Error(), Waits: waits(waitsLKG, err)}
		return out, err
	}
	f := &finishing{h: h, due: due, feature: req.Feature, summary: req.Summary, candidate: req.Candidate,
		receipt: req.Receipt, evidence: evidence}
	switch d.verdict {
	case lkgUnjudged:
		return p.blockVerified(ctx, h, req.Artifact.Unit, d)
	case lkgSuperseded:
		f.superseded = d.reason
	}
	return p.complete(ctx, f)
}

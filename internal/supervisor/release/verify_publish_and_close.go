package release

import (
	"context"
	"strconv"

	"github.com/bestagentkits/sisf/internal/catalog"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
)

// finish closes a release that passed its observation window. The revision
// is read from the running system once more after the window: only the
// release itself, healthy, is verified. The verified release then becomes
// the LKG, the changelog and the catalog are published by the Publisher
// identity, the change is landed on the default branch (see Lander), and
// the issue closes. A step that cannot be taken leaves the
// issue where it is, and the outcome says so.
func (p *Pipeline) finish(ctx context.Context, r *rollout) (Outcome, error) {
	out := Outcome{Release: r.rev.Digest, Windows: r.windows, State: r.h.record.State}
	held := func(err error) (Outcome, error) {
		out.Result, out.Reason, out.State = ResultHeld, err.Error(), r.h.record.State
		return out, err
	}
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
	err = p.step(ctx, r.h, stepSpec{
		event: core.EventPostDeployVerified, reason: "the observed release met its DoD through the observation window",
		release: r.rev.Digest, evidence: []string{"observed:" + string(observed.Digest)},
		guards: statemachine.Guards{PostDeploy: statemachine.PostDeployGuardFunc(func(context.Context, core.Transition) (statemachine.PostDeployProof, error) {
			return proof, nil
		})},
	})
	if err != nil {
		// The release served through its window but its verification could
		// not be recorded, so it cannot be left in Monitoring unwatched.
		return p.restore(ctx, r, undo{cause: statemachine.BlockReleaseInterrupted,
			reason: "the verified release could not be recorded, so it could not be kept: " + err.Error()})
	}
	if err := p.cfg.LKG.record(r.v); err != nil {
		return held(err)
	}
	if p.frozen(ctx) {
		return held(core.NewError(core.CodeFrozen, "an emergency stop froze publishing; the verified release is not published"))
	}
	ref, err := p.cfg.Publisher.Publish(ctx, catalog.Entry{
		IssueID: r.h.record.IssueID, RunID: r.h.record.RunID, Unit: r.unit, Release: r.rev.Digest,
		Feature: r.feature, Summary: r.summary, VerifiedAt: p.cfg.Clock.Now().UTC(),
	})
	if err != nil {
		return held(err)
	}
	out.PublicationRef = ref
	landing, err := p.land(ctx, LandRequest{IssueID: r.h.record.IssueID, RunID: r.h.record.RunID, Release: r.rev.Digest,
		Candidate: r.candidate, Receipt: r.v.receipt})
	if err != nil {
		return held(err)
	}
	if err := p.closeLanded(ctx, r.h, landing, []string{"observed:" + string(observed.Digest), ref}); err != nil {
		return held(err)
	}
	out.Result, out.State = ResultClosed, r.h.record.State
	return out, p.audit(ctx, r.h, "release.promote", core.OutcomeExecuted, map[string]string{
		"release": string(r.rev.Digest), "publication": ref,
	})
}

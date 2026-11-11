package release

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
)

// LandingDueKey is the key, in RecoveryNamespace, of the landing a
// verified release of the run still owes.
func LandingDueKey(run core.RunID) string { return "landing-due/" + string(run) }

func putLandingDue(ctx context.Context, tx state.Tx, req LandRequest) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return invalid("the landing cannot be encoded").WithCause(err)
	}
	version := int64(0)
	if existing, err := tx.GetKV(ctx, RecoveryNamespace, LandingDueKey(req.RunID)); err == nil {
		version = existing.Version
	} else if !core.HasCode(err, core.CodeNotFound) {
		return err
	}
	_, err = tx.PutKV(ctx, state.KVEntry{Namespace: RecoveryNamespace, Key: LandingDueKey(req.RunID), Value: raw, Version: version})
	return err
}

// deleteLandingDue forgets the landing the run owed, once its issue closes
// on the decided landing; a run that owed none has nothing to forget.
func deleteLandingDue(ctx context.Context, tx state.Tx, run core.RunID) error {
	existing, err := tx.GetKV(ctx, RecoveryNamespace, LandingDueKey(run))
	if core.HasCode(err, core.CodeNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return tx.DeleteKV(ctx, RecoveryNamespace, LandingDueKey(run), existing.Version)
}

// ReadLandingDue reads the landing the verified release of the run owes;
// NOT_FOUND when its landing was never reached.
func ReadLandingDue(ctx context.Context, tx state.Tx, run core.RunID) (LandRequest, error) {
	entry, err := tx.GetKV(ctx, RecoveryNamespace, LandingDueKey(run))
	if err != nil {
		return LandRequest{}, err
	}
	var req LandRequest
	if err := json.Unmarshal(entry.Value, &req); err != nil || req.RunID != run {
		return LandRequest{}, invalid("the landing due for %s cannot be read", run)
	}
	return req, nil
}

// LandRequest asks to land the change of a verified release on the
// repository's default branch.
type LandRequest struct {
	IssueID core.IssueRef      `json:"issue_id"`
	RunID   core.RunID         `json:"run_id"`
	Release core.ReleaseDigest `json:"release"`
	// Candidate and Receipt are the candidate as QA judged it and its
	// signed receipt: the release was built from Candidate.CommitSHA.
	Candidate qa.Candidate   `json:"candidate"`
	Receipt   core.QAReceipt `json:"receipt"`
}

// Landing is what became of the change: merged, or left for the Owner
// with the reason in Summary.
type Landing struct {
	Merged  bool
	Ref     string
	Summary string
}

// Lander lands a verified release's change. It decides once and records
// what it decided: a merge it may not make, or that GitHub refuses, is a
// Landing that is not merged. An error means nothing could be decided now
// (GitHub is out, say); the issue then stays Verified and the landing is
// tried again.
type Lander interface {
	Land(ctx context.Context, req LandRequest) (Landing, error)
}

// land lands the change of a verified release before its issue closes. It
// first records that the landing is due, so an issue held here by an
// emergency stop, an outage of GitHub or a restart is finished later by
// FinishVerified. An emergency stop holds it: nothing is merged while one
// is in force.
func (p *Pipeline) land(ctx context.Context, req LandRequest) (Landing, error) {
	if p.cfg.Lander == nil {
		return Landing{}, nil
	}
	if err := p.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error { return putLandingDue(ctx, tx, req) }); err != nil {
		return Landing{}, err
	}
	if p.frozen(ctx) {
		return Landing{}, core.NewError(core.CodeFrozen, "an emergency stop froze publishing; the verified change is not merged")
	}
	return p.cfg.Lander.Land(ctx, req)
}

// closeLanded closes the issue of a verified release after its landing.
// The closing step says whether the change was merged, and forgets in the
// same transaction that the landing was due: it is decided.
func (p *Pipeline) closeLanded(ctx context.Context, h *holding, landing Landing, evidence []string) error {
	reason := "verified, recorded as the LKG, changelog and catalog published"
	if landing.Summary != "" {
		reason += "; " + landing.Summary
	}
	if landing.Ref != "" {
		evidence = append(evidence, landing.Ref)
	}
	return p.step(ctx, h, stepSpec{event: core.EventIssueClosed, reason: reason, evidence: evidence,
		also: func(ctx context.Context, tx state.Tx, r core.StateRecord) error {
			return deleteLandingDue(ctx, tx, r.RunID)
		}})
}

// FinishVerified lands and closes an issue left Verified because its
// landing could not be decided, for example after a restart, an emergency
// stop or an outage of GitHub between the verification and the merge. Only
// an issue whose landing is due (see ReadLandingDue) is resumed: everything
// before the landing already happened.
func (p *Pipeline) FinishVerified(ctx context.Context, req LandRequest) (Outcome, error) {
	out := Outcome{Release: req.Release}
	stop := func(result Result, err error) (Outcome, error) {
		out.Result, out.Reason = result, err.Error()
		return out, err
	}
	h, err := p.take(ctx, req.IssueID)
	if err != nil {
		return stop(ResultHeld, err)
	}
	defer p.letGo(ctx, h)
	out.State = h.record.State
	if h.record.State != core.StateVerified || h.record.RunID != req.RunID || h.record.ReleaseDigest != req.Release {
		return stop(ResultRefused, core.NewError(core.CodeConflict, "the issue is not verified with this run's release"))
	}
	landing, err := p.land(ctx, req)
	if err != nil {
		return stop(ResultHeld, err)
	}
	if err := p.closeLanded(ctx, h, landing, []string{"release:" + string(req.Release)}); err != nil {
		return stop(ResultHeld, err)
	}
	out.Result, out.State = ResultClosed, h.record.State
	return out, p.audit(ctx, h, "release.land", core.OutcomeExecuted, map[string]string{"release": string(req.Release), "merged": strconv.FormatBool(landing.Merged)})
}

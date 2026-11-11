package release

import (
	"context"
	"encoding/json"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
)

// LandingDueKey is the key, in RecoveryNamespace, of the landing a
// verified release of the run owed, as an earlier build of the supervisor
// recorded it before merging; FinishDueKey has replaced it.
func LandingDueKey(run core.RunID) string { return "landing-due/" + string(run) }

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

// ReadLandingDue reads the landing an earlier build of the supervisor
// recorded as due for the verified release of the run; NOT_FOUND when it
// recorded none.
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

// land lands the change of a verified release before its issue closes.
// What the release still owes is recorded as due (FinishDue) since its
// verification, so an issue held here by an emergency stop, an outage of
// GitHub or a restart is finished later by FinishVerified. An emergency
// stop holds it: nothing is merged while one is in force.
func (p *Pipeline) land(ctx context.Context, req LandRequest) (Landing, error) {
	if p.cfg.Lander == nil {
		return Landing{}, nil
	}
	if p.frozen(ctx) {
		return Landing{}, core.NewError(core.CodeFrozen, "an emergency stop froze publishing; the verified change is not merged")
	}
	return p.cfg.Lander.Land(ctx, req)
}

// closeLanded closes the issue of a verified release after its landing.
// The closing step says whether the change was merged, and forgets in the
// same transaction what the release owed: it is all done.
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
			if err := deleteFinishDue(ctx, tx, r.RunID); err != nil {
				return err
			}
			return deleteLandingDue(ctx, tx, r.RunID)
		}})
}

package release

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Block is why the pipeline blocked an issue: its release was refused
// before anything was deployed, or it was restored to the LKG. It is
// recorded in the transaction that writes the step to Blocked, and the
// Owner's retry is judged by its cause, never by the request to retry.
type Block struct {
	IssueID core.IssueRef
	RunID   core.RunID
	Attempt int
	Cause   statemachine.BlockCause
	// Code is the code a refused release was refused with; empty after a
	// restore.
	Code       core.ErrorCode
	Release    core.ReleaseDigest
	RestoredTo core.ReleaseDigest
	// Verified is set when the release was verified before it could no
	// longer be judged: it was not finished, and its change not merged.
	Verified  bool
	BlockedAt time.Time
}

// BlockRecorder keeps the record of why the pipeline blocked an issue, in
// the transaction that blocks it, where the Owner's retry and the issue's
// progress read it.
type BlockRecorder interface {
	RecordBlock(ctx context.Context, kv state.KV, b Block) error
}

// undo is why a release that started its rollout is restored to the LKG,
// and the cause the issue is blocked with afterwards.
type undo struct {
	cause  statemachine.BlockCause
	reason string
}

// judgedUnhealthy is the undo of a release the rollback rules judged.
func judgedUnhealthy(reason string) *undo {
	return &undo{cause: statemachine.BlockRolledBack, reason: reason}
}

// refusal checks what the pipeline refuses a candidate for before anything
// is deployed: a request that does not hold together, a change GitHub may
// list only part of, a change of the trusted control surface, a unit the
// factory may not publish by itself, a receipt or artifact that does not
// verify, an expired one included, and a base that does not contain the
// LKG release of the unit (checkBaseHasLKG).
func (p *Pipeline) refusal(ctx context.Context, req Request, record core.StateRecord) (verifiedRelease, error) {
	if err := req.validate(); err != nil {
		return verifiedRelease{}, err
	}
	if req.Candidate.ChangedPathsIncomplete {
		return verifiedRelease{}, governance("GitHub may list only part of the paths the candidate changes, so it cannot be shown to leave the trusted control surface alone")
	}
	if err := checkGovernance(p.cfg.Protected, p.cfg.UnitInputs, req.Artifact.Unit, req.Candidate.ChangedPaths); err != nil {
		return verifiedRelease{}, err
	}
	if err := checkSelfPublishable(p.cfg.SelfPublishable, req.Artifact.Unit); err != nil {
		return verifiedRelease{}, err
	}
	v, err := p.verify(ctx, req, record)
	if err != nil {
		_ = p.audit(ctx, &holding{record: record}, "release.verify", core.OutcomeDenied, map[string]string{"code": string(core.CodeOf(err))})
		return v, err
	}
	return v, p.checkBaseHasLKG(ctx, v)
}

// judgedRefusal reports whether a refusal judged the candidate or the
// request, so that trying again cannot change it: the candidate changes
// the trusted surface, is a unit the factory may not publish, or its
// receipt or build is expired, forged, unknown or not the one QA judged.
// Any other failure, such as a store or key trust that could not be read
// or a context that ended, judged nothing, and the release is tried again
// on the next pass instead.
func judgedRefusal(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	switch core.CodeOf(err) {
	case core.CodeApprovalRequired, core.CodeExpired, core.CodeSignatureInvalid, core.CodeTampered, core.CodeGuardFailed,
		core.CodeInvalidArgument, core.CodePermissionDenied, core.CodeNotFound, core.CodeReplayed:
		return true
	}
	return false
}

// refuse blocks the issue whose release was refused, with the refusal's
// code recorded as the cause's detail. Nothing was deployed. When the step
// cannot be written the issue stays in ReleaseCandidate and the outcome
// says so, so the release is judged again later rather than forgotten.
func (p *Pipeline) refuse(ctx context.Context, h *holding, out Outcome, refused error) (Outcome, error) {
	out.Result, out.Reason, out.State = ResultRefused, refused.Error(), h.record.State
	block := Block{
		IssueID: h.record.IssueID, RunID: h.record.RunID, Attempt: h.record.Attempt, Cause: statemachine.BlockReleaseRefused,
		Code: core.CodeOf(refused), Release: out.Release, BlockedAt: p.cfg.Clock.Now().UTC(),
	}
	err := p.step(ctx, h, stepSpec{
		event: core.EventReleaseRefused, reason: "the release was refused before anything was deployed: " + string(block.Code),
		also: func(ctx context.Context, tx state.Tx, _ core.StateRecord) error {
			return p.cfg.Blocks.RecordBlock(ctx, tx, block)
		},
	})
	out.State = h.record.State
	if err != nil {
		return out, core.NewError(core.CodeOf(refused), refused.Error()+"; the issue could not be blocked yet: "+err.Error()).WithCause(refused)
	}
	out.Cause = statemachine.BlockReleaseRefused
	_ = p.audit(ctx, h, "release.refuse", core.OutcomeDenied, map[string]string{"code": string(block.Code)})
	return out, refused
}

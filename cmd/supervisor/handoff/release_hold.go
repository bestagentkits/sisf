package handoff

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Results of a release that did not go ahead, as the release pipeline
// names them (release.ResultHeld, release.ResultRefused).
const (
	ReleaseResultHeld    = "held"
	ReleaseResultRefused = "refused"
)

// ReleaseHold is the supervisor's record of a release of a candidate that
// was held or refused, kept in the build namespace beside the candidate's
// other records. A held or refused release is not tried again by itself:
// the issue waits in ReleaseCandidate. Only the Owner's retry of the issue
// lets a held release start over (ResumeHeldRelease); a refused one is
// final for its candidate, and what is left to the Owner is to change the
// policy in a governed update or to close the issue.
//
// Reason is the pipeline's own sentence, for the Owner's authenticated
// surfaces and the supervisor's log. It can name units, backends and what
// failed, so what is shown on the issue is Summary, chosen by the result
// and the code alone.
type ReleaseHold struct {
	IssueID core.IssueRef      `json:"issue_id"`
	RunID   core.RunID         `json:"run_id"`
	Attempt int                `json:"attempt"`
	Result  string             `json:"result"`
	Code    core.ErrorCode     `json:"code"`
	Reason  string             `json:"reason,omitempty"`
	Release core.ReleaseDigest `json:"release,omitempty"`
	HeldAt  time.Time          `json:"held_at"`
	// ResumedBy and ResumedAt name the Owner's retry that lets the held
	// release start over; the supervisor then releases the candidate again.
	ResumedBy string    `json:"resumed_by,omitempty"`
	ResumedAt time.Time `json:"resumed_at,omitzero"`
}

// ReleaseHoldKey is the key of the hold of one evaluator attempt's release.
func ReleaseHoldKey(run core.RunID, attempt int) string {
	return fmt.Sprintf("release-hold/%s/%d", run, attempt)
}

// PutReleaseHold records a held or refused release, replacing an earlier
// one of the same attempt.
func PutReleaseHold(ctx context.Context, kv state.KV, h ReleaseHold) error {
	return putReplacing(ctx, kv, ReleaseHoldKey(h.RunID, h.Attempt), h)
}

// ReadReleaseHold reads the hold of one attempt's release, NOT_FOUND when
// its release was not held or refused.
func ReadReleaseHold(ctx context.Context, kv state.KV, run core.RunID, attempt int) (ReleaseHold, error) {
	entry, err := kv.GetKV(ctx, BuildNamespace, ReleaseHoldKey(run, attempt))
	if err != nil {
		return ReleaseHold{}, err
	}
	var h ReleaseHold
	if json.Unmarshal(entry.Value, &h) != nil || h.RunID != run || h.Attempt != attempt {
		return ReleaseHold{}, core.NewError(core.CodeTampered, "the stored release hold cannot be read")
	}
	return h, nil
}

// DropReleaseHold forgets the hold of one attempt's release once a later
// release of it went ahead.
func DropReleaseHold(ctx context.Context, kv state.KV, run core.RunID, attempt int) error {
	entry, err := kv.GetKV(ctx, BuildNamespace, ReleaseHoldKey(run, attempt))
	if core.HasCode(err, core.CodeNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return kv.DeleteKV(ctx, BuildNamespace, entry.Key, entry.Version)
}

// Waiting reports whether the release waits for a person: it was refused,
// or it was held and the Owner has not resumed it yet.
func (h ReleaseHold) Waiting() bool {
	return h.Result == ReleaseResultRefused || h.ResumedAt.IsZero()
}

// Resumed reports whether the Owner resumed the held release, so the
// supervisor releases the candidate again.
func (h ReleaseHold) Resumed() bool {
	return h.Result == ReleaseResultHeld && !h.ResumedAt.IsZero()
}

// ResumeHeldRelease lets a held release start over on the Owner's retry.
// A release with no hold is NOT_FOUND; a refused one, or one already
// resumed, is CONFLICT.
func ResumeHeldRelease(ctx context.Context, kv state.KV, run core.RunID, attempt int, owner string, now time.Time) error {
	h, err := ReadReleaseHold(ctx, kv, run, attempt)
	if err != nil {
		return err
	}
	switch {
	case h.Result != ReleaseResultHeld:
		return core.NewError(core.CodeConflict, "the release was refused, not held; a refused release is never tried again")
	case h.Resumed():
		return core.NewError(core.CodeConflict, "the held release was already resumed and is being released again")
	}
	h.ResumedBy, h.ResumedAt = owner, now
	return PutReleaseHold(ctx, kv, h)
}

// Summary says in words safe to publish why the release does not go ahead
// and where the issue waits. It is chosen by the result and the code only.
func (h ReleaseHold) Summary() string {
	if h.Result == ReleaseResultRefused {
		why := "the release was refused before anything was deployed"
		if h.Code == core.CodeApprovalRequired {
			why = "the change touches the factory's trusted control surface, or a unit the policy does not let the factory publish by itself, which only a person may publish"
		}
		return fmt.Sprintf("The release of run %s was refused: %s. The refusal is final for this candidate and it is never tried again; "+
			"the issue waits in ReleaseCandidate until the Owner changes the policy in a governed update or closes the issue.", h.RunID, why)
	}
	var why string
	switch h.Code {
	case core.CodeFrozen:
		why = "an emergency stop holds publishing"
	case core.CodeReleaseHold:
		why = "the factory could not yet show that the release can be observed, judged and rolled back safely, so nothing was deployed"
	default:
		why = "the release could not start"
	}
	return fmt.Sprintf("The release of run %s is held: %s. The issue waits in ReleaseCandidate until the Owner retries it.", h.RunID, why)
}

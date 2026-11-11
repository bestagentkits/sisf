package handoff

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

// RunStop is the control zone's record of why it blocked the issue of a
// run: the run stopped without a candidate, or the supervisor refused, rolled
// back or could not finish the run's release. It is written in the
// transaction that writes the step to Blocked and kept in the build
// namespace, which only the control zone writes, under the run it is about:
// a run blocks its issue once, and a later block of the issue belongs to
// another run and has a cause of its own. The retry guard reads the cause
// of the block from it, never from the request.
//
// It holds codes, digests and numbers only, so Summary can be shown on the
// issue.
type RunStop struct {
	IssueID core.IssueRef `json:"issue_id"`
	RunID   core.RunID    `json:"run_id"`
	// Attempt is the evaluator attempt the record of the issue stood at.
	Attempt int                     `json:"attempt"`
	Cause   statemachine.BlockCause `json:"cause"`
	// Attempts is how many coding attempts the run started, and LastCode
	// the code its last failed one ended with; "" when it is not known.
	// Both are what the agent zone recorded, so they are shown as a number
	// and a known code or not at all.
	Attempts int            `json:"attempts,omitempty"`
	LastCode core.ErrorCode `json:"last_code,omitempty"`
	// ReleaseCode is the code the supervisor refused the run's release
	// with; Release is the release that was refused or undone, and
	// RestoredTo the last known good release that serves again.
	ReleaseCode core.ErrorCode     `json:"release_code,omitempty"`
	Release     core.ReleaseDigest `json:"release,omitempty"`
	RestoredTo  core.ReleaseDigest `json:"restored_to,omitempty"`
	BlockedAt   time.Time          `json:"blocked_at"`
}

// RunStopKey is the key of the record of one stopped run.
func RunStopKey(run core.RunID) string { return "run-stop/" + string(run) }

// PutRunStop records why an issue is blocked, in the transaction that
// blocks it.
func PutRunStop(ctx context.Context, kv state.KV, s RunStop) error {
	return putReplacing(ctx, kv, RunStopKey(s.RunID), s)
}

// ReadRunStop reads the record of a stopped run, NOT_FOUND when the
// control zone did not block an issue for it.
func ReadRunStop(ctx context.Context, kv state.KV, run core.RunID) (RunStop, error) {
	entry, err := kv.GetKV(ctx, BuildNamespace, RunStopKey(run))
	if err != nil {
		return RunStop{}, err
	}
	var s RunStop
	if json.Unmarshal(entry.Value, &s) != nil || s.RunID != run || !s.Cause.Valid() {
		return RunStop{}, core.NewError(core.CodeTampered, "the stored record of a stopped run cannot be read")
	}
	return s, nil
}

// Summary says in words safe to publish why the issue is blocked. It is
// chosen by the cause and the recorded code and number only.
func (s RunStop) Summary() string {
	code := "an unrecognised code"
	if s.LastCode.Valid() {
		code = "`" + string(s.LastCode) + "`"
	}
	switch s.Cause {
	case statemachine.BlockRunExhausted:
		return fmt.Sprintf("Run %s used all %d coding attempts it was allowed without a candidate; the last one failed with %s.",
			s.RunID, s.Attempts, code)
	case statemachine.BlockPolicySuperseded:
		return fmt.Sprintf("Run %s was coded under a policy that is no longer in force, so its patch is not built, judged or released.", s.RunID)
	case statemachine.BlockReleaseRefused:
		return fmt.Sprintf("The release of run %s was refused before anything was deployed: %s.", s.RunID, s.refusal())
	case statemachine.BlockRolledBack:
		return fmt.Sprintf("The release of run %s was judged unhealthy while it served traffic and was rolled back; "+
			"the last known good release serves again and the recovery report has the evidence.", s.RunID)
	case statemachine.BlockReleaseInterrupted:
		return fmt.Sprintf("The release of run %s was stopped before it could be judged, because the supervisor stopped or could not go on with it; "+
			"it was rolled back and the last known good release serves again. Nothing is known about whether the release was healthy.", s.RunID)
	case statemachine.BlockReleaseUnjudged:
		return fmt.Sprintf("The release of run %s could not be judged while it served traffic, because it could not be observed or measured "+
			"or its synthetic suite could not be run; it was rolled back and the last known good release serves again.", s.RunID)
	}
	return fmt.Sprintf("Run %s stopped without a candidate.", s.RunID)
}

// refusal says why a release was refused, by the code it was refused with.
func (s RunStop) refusal() string {
	switch s.ReleaseCode {
	case core.CodeExpired:
		return "its QA receipt, or the key that signed the receipt or the build, has expired, so the candidate can no longer be verified"
	case core.CodeApprovalRequired:
		return "it changes the factory's trusted control surface, or it is a unit the policy does not let the factory publish by itself, " +
			"which only a person may publish"
	case core.CodeSignatureInvalid, core.CodeTampered, core.CodeGuardFailed:
		return "its QA receipt or its build did not verify as the candidate QA passed, signed by this factory"
	}
	return "the request to release it did not hold together"
}

// WayOut says how the Owner gets the issue going again.
func (s RunStop) WayOut() string {
	retry := fmt.Sprintf("as the Owner on the factory's host, run `factoryd issue retry --issue %s`", s.IssueID)
	switch s.Cause {
	case statemachine.BlockPolicySuperseded:
		return "A" + retry[1:] + ": the scope is judged under the policy in force and, once it is permitted or approved under it, " +
			"the issue returns to Ready and a new run codes it under that policy."
	case statemachine.BlockReleaseRefused:
		if s.ReleaseCode == core.CodeApprovalRequired {
			return "A change of the trusted control surface, or of a unit outside release.units, is published only in a governance update a person makes. " +
				"To have the factory code the issue again within what it may publish, " + retry +
				": the issue returns to Ready and a new run codes, judges and releases it, which spends agent budget. Otherwise close the issue."
		}
		return "A" + retry[1:] + ": the issue returns to Ready and a new run codes, judges and releases it afresh, which spends agent budget."
	case statemachine.BlockRolledBack, statemachine.BlockReleaseInterrupted, statemachine.BlockReleaseUnjudged:
		return "Read the recovery report and remove the cause, then, " + retry +
			": the issue returns to Ready and a new run codes, judges and releases it, which spends agent budget."
	}
	return "Remove the cause, then, " + retry + ": the issue returns to Ready and a new run claims it."
}

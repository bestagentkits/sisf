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

// RunStop is the control zone's record of why it blocked an issue whose
// run stopped without a candidate. It is written in the transaction that
// writes the step to Blocked and kept in the build namespace, which only
// the control zone writes, under the run it is about: a later block of
// the issue belongs to another run and has a cause of its own. The retry
// guard reads the cause of the block from it, never from the request.
//
// It holds codes and numbers only, so Summary can be shown on the issue.
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
	Attempts  int            `json:"attempts,omitempty"`
	LastCode  core.ErrorCode `json:"last_code,omitempty"`
	BlockedAt time.Time      `json:"blocked_at"`
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
	}
	return fmt.Sprintf("Run %s stopped without a candidate.", s.RunID)
}

// WayOut says how the Owner gets the issue going again.
func (s RunStop) WayOut() string {
	return fmt.Sprintf("Remove the cause, then, as the Owner on the factory's host, run `factoryd issue retry --issue %s`: "+
		"the issue returns to Ready and a new run claims it.", s.IssueID)
}

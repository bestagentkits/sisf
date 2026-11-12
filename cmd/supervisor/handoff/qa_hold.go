package handoff

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// TopicQAHeld is the outbox topic an earlier release queued a separate
// notice of a hold under. Holds are shown in the issue's progress comment
// now; a message still queued under it is drained without a comment.
const TopicQAHeld = "qa.held"

// QAHoldCause says why QA held a candidate.
type QAHoldCause string

const (
	// QAHoldAttemptsUsed: the last attempt the run was allowed failed QA,
	// so the run starts no more coding attempts.
	QAHoldAttemptsUsed QAHoldCause = "attempts_used"
	// QAHoldOwnerApproval: QA passed on the coder's own model route and the
	// policy wants the Owner to approve the release.
	QAHoldOwnerApproval QAHoldCause = "owner_approval"
)

// QAHold is the supervisor's record of a held verdict, kept in the build
// namespace next to the verdict itself. It names gates and numbers only,
// never the text of a refusal, so it can be shown on the issue as it is.
type QAHold struct {
	IssueID     core.IssueRef `json:"issue_id"`
	RunID       core.RunID    `json:"run_id"`
	Attempt     int           `json:"attempt"`
	MaxAttempts int           `json:"max_attempts"`
	Cause       QAHoldCause   `json:"cause"`
	// Built is false when the patch could not be built; Receipt is false
	// when the evaluation issued no signed receipt.
	Built   bool `json:"built"`
	Receipt bool `json:"receipt"`
	// FailedGates are the hard gates the receipt records as failed.
	FailedGates []string  `json:"failed_gates,omitempty"`
	Score       int       `json:"score"`
	PassScore   int       `json:"pass_score"`
	HeldAt      time.Time `json:"held_at"`
}

// QAHoldKey is the key of the hold of one evaluator attempt.
func QAHoldKey(run core.RunID, attempt int) string { return fmt.Sprintf("qa-hold/%s/%d", run, attempt) }

// PutQAHold records a hold in the transaction that records the verdict.
// The issue's progress comment shows it; nothing more is queued.
func PutQAHold(ctx context.Context, tx state.Tx, h QAHold) error {
	return putReplacing(ctx, tx, QAHoldKey(h.RunID, h.Attempt), h)
}

// ReadQAHold reads the hold of one evaluator attempt, NOT_FOUND when QA
// did not hold it.
func ReadQAHold(ctx context.Context, kv state.KV, run core.RunID, attempt int) (QAHold, error) {
	entry, err := kv.GetKV(ctx, BuildNamespace, QAHoldKey(run, attempt))
	if err != nil {
		return QAHold{}, err
	}
	var h QAHold
	if json.Unmarshal(entry.Value, &h) != nil || h.RunID != run || h.Attempt != attempt {
		return QAHold{}, core.NewError(core.CodeTampered, "the stored QA hold cannot be read")
	}
	return h, nil
}

// Summary says in one sentence why the candidate was held, from the
// recorded numbers and gate names only.
func (h QAHold) Summary() string {
	var why string
	switch {
	case !h.Built:
		why = "its patch could not be built"
	case !h.Receipt:
		why = "the evaluation issued no signed receipt"
	case len(h.FailedGates) > 0:
		why = fmt.Sprintf("hard gate %s failed (score %d, pass mark %d)", strings.Join(h.FailedGates, ", "), h.Score, h.PassScore)
	default:
		why = fmt.Sprintf("score %d against the pass mark %d", h.Score, h.PassScore)
	}
	if h.Cause == QAHoldOwnerApproval {
		return fmt.Sprintf("QA passed attempt %d of run %s with score %d on the coder's own model route; the policy wants the Owner to approve the release, and the issue waits in QAEvaluation.",
			h.Attempt, h.RunID, h.Score)
	}
	return fmt.Sprintf("QA failed attempt %d of %d of run %s: %s. The run starts no more coding attempts, and the issue waits in QAEvaluation.",
		h.Attempt, h.MaxAttempts, h.RunID, why)
}

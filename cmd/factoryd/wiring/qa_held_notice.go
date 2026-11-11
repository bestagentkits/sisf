package wiring

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// qaHeldMarker identifies the notice of the hold of one evaluator attempt.
func qaHeldMarker(h handoff.QAHold) string {
	return "<!-- sisf:qa-held " + string(h.RunID) + "/" + strconv.Itoa(h.Attempt) + " -->"
}

// deliverQAHeld posts the supervisor's notice that QA held a candidate on
// the issue, once per hold.
func (c *Control) deliverQAHeld(ctx context.Context, m state.OutboxMessage) error {
	var h handoff.QAHold
	if err := json.Unmarshal(m.Payload, &h); err != nil || h.IssueID.Validate() != nil || h.RunID.Validate() != nil || h.Attempt < 1 {
		return core.NewError(core.CodeInvalidArgument, "the QA hold notice cannot be read")
	}
	comments, err := c.cfg.GitHub.ListComments(ctx, h.IssueID, time.Time{})
	if err != nil {
		return err
	}
	marker := qaHeldMarker(h)
	for _, comment := range comments {
		if strings.Contains(comment.Body, marker) {
			return nil
		}
	}
	_, err = c.cfg.GitHub.CreateComment(ctx, h.IssueID, qaHeldComment(h)+"\n\n"+marker)
	return err
}

// qaHeldComment is the notice as the Owner reads it. It is built from the
// hold's numbers and gate names only.
func qaHeldComment(h handoff.QAHold) string {
	return "### Candidate held after QA\n\n" + h.Summary() + "\n\n" + qaHoldNext
}

// qaHoldNext says what can happen next. The lifecycle has no step out of
// QAEvaluation but back to development, which a held run may not take, so
// an Owner retry is refused; the notice says so rather than promise one.
const qaHoldNext = "The lifecycle has no step out of a held evaluation yet: an Owner retry of this issue is refused. " +
	"`runs.explain` shows the evidence; the supervisor's QA record of the attempt holds the signed receipt."

// storeQAHolds reads the supervisor's QA holds for runs.explain.
type storeQAHolds struct{ store state.Store }

func (s storeQAHolds) QAHold(ctx context.Context, run core.RunID, attempt int) (runactions.QAHold, error) {
	var h handoff.QAHold
	err := s.store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		h, err = handoff.ReadQAHold(ctx, tx, run, attempt)
		return err
	})
	if err != nil {
		return runactions.QAHold{}, err
	}
	return runactions.QAHold{AwaitsApproval: h.Cause == handoff.QAHoldOwnerApproval, Summary: h.Summary(),
		Ref: handoff.BuildNamespace + ":" + handoff.QAHoldKey(run, attempt), At: h.HeldAt}, nil
}

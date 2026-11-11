package runactions

import (
	"context"
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Cause classifies where an issue's run stands.
type Cause string

const (
	CauseBlocked             Cause = "blocked"
	CauseRolledBack          Cause = "rolled_back"
	CauseAwaitingApproval    Cause = "awaiting_approval"
	CauseAwaitingInformation Cause = "awaiting_information"
	CauseAttemptsExhausted   Cause = "attempts_exhausted"
	CauseFinished            Cause = "finished"
	CauseInProgress          Cause = "in_progress"
)

// Evidence is one fact an explanation rests on, with where to find it.
type Evidence struct {
	Kind    string    `json:"kind"`
	Ref     string    `json:"ref"`
	Summary string    `json:"summary"`
	At      time.Time `json:"at,omitzero"`
}

// Explanation answers why a run is stopped or blocked.
type Explanation struct {
	Issue       string            `json:"issue"`
	State       core.State        `json:"state"`
	RunID       core.RunID        `json:"run_id,omitempty"`
	Stopped     bool              `json:"stopped"`
	Cause       Cause             `json:"cause"`
	Reason      string            `json:"reason"`
	Evidence    []Evidence        `json:"evidence"`
	NextActions []core.NextAction `json:"next_actions,omitempty"`
}

// maxApprovalEvidence bounds the approval requests one explanation cites.
const maxApprovalEvidence = 5

func (h handlers) runsExplain(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	target, err := h.resolve(ctx, req)
	if err != nil {
		return core.ActionResult{}, err
	}
	requests, err := h.Approvals.List(ctx, approvals.Filter{IssueID: target.Record.IssueID})
	if err != nil {
		return core.ActionResult{}, err
	}
	if len(requests) > maxApprovalEvidence {
		requests = requests[len(requests)-maxApprovalEvidence:]
	}
	out, err := h.explain(ctx, target, requests)
	if err != nil {
		return core.ActionResult{}, err
	}
	return actions.JSONResult(out)
}

// explain judges the record, the run and the approvals of one issue.
func (h handlers) explain(ctx context.Context, t Inspection, requests []approvals.Request) (Explanation, error) {
	r := t.Record
	issue := r.IssueID.String()
	out := Explanation{Issue: issue, State: r.State, RunID: r.RunID, Evidence: []Evidence{{
		Kind: "state_record", Ref: "issue:" + issue, At: r.UpdatedAt,
		Summary: fmt.Sprintf("state %s at version %d, attempt %d", r.State, r.Version, r.Attempt)}}}
	if r.ReleaseDigest != "" {
		out.Evidence = append(out.Evidence, Evidence{Kind: "release", Ref: string(r.ReleaseDigest), Summary: "release rolled out by the run"})
	}
	exhausted := t.Run != nil && t.Run.Status == dispatcher.RunExhausted
	if t.Run != nil {
		out.Evidence = append(out.Evidence, runEvidence(*t.Run)...)
	}
	pending := ""
	for _, req := range requests {
		e, err := h.approvalEvidence(ctx, req)
		if err != nil {
			return Explanation{}, err
		}
		out.Evidence = append(out.Evidence, e...)
		if req.Status == approvals.StatusPending && pending == "" {
			pending = req.ID
		}
	}
	hold, err := h.qaHold(ctx, r)
	if err != nil {
		return Explanation{}, err
	}
	about := map[string]string{"issue": issue}
	inspect := core.NextAction{Action: ActionRunsInspect, Description: "read the full records", Params: about}
	switch {
	case r.State == core.StateBlocked:
		out.Stopped, out.Cause = true, CauseBlocked
		out.Reason = "The issue is blocked; it moves on only through a safe, authorized retry."
		out.NextActions = []core.NextAction{{Action: "approvals.list", Description: "see the approvals of this issue", Params: about}, inspect}
	case r.State == core.StateRolledBack:
		out.Stopped, out.Cause = true, CauseRolledBack
		out.Reason = "The release was rolled back; the issue is blocked once the recovery report is recorded."
		out.NextActions = []core.NextAction{inspect}
	case r.State == core.StateApprovalPending:
		out.Stopped, out.Cause = true, CauseAwaitingApproval
		out.Reason = "The issue waits for the Owner to approve its scope."
		out.NextActions = approvalSteps(pending, inspect)
	case r.State == core.StateClarifying:
		out.Stopped, out.Cause = true, CauseAwaitingInformation
		out.Reason = "The issue waits for the reporter to answer the clarifying questions on GitHub."
		out.NextActions = []core.NextAction{inspect}
	case hold != nil:
		out.Stopped, out.Cause = true, CauseAttemptsExhausted
		if hold.AwaitsApproval {
			out.Cause = CauseAwaitingApproval
		}
		out.Reason = hold.Summary + " The lifecycle has no step out of a held evaluation yet, so an Owner retry is refused."
		out.Evidence = append(out.Evidence, Evidence{Kind: "qa_hold", Ref: hold.Ref, Summary: hold.Summary, At: hold.At})
		out.NextActions = []core.NextAction{inspect}
	case exhausted:
		out.Stopped, out.Cause = true, CauseAttemptsExhausted
		out.Reason = fmt.Sprintf("The run used all %d attempts it was allowed; nothing more is started for it.", t.Run.Attempt)
		out.NextActions = []core.NextAction{inspect}
	case r.State == core.StateVerified || r.State == core.StateClosed:
		out.Cause, out.Reason = CauseFinished, "The issue's run finished: the deployed version met its definition of done."
	default:
		out.Cause, out.Reason = CauseInProgress, "The run is not stopped; the issue is in "+string(r.State)+"."
		out.NextActions = []core.NextAction{inspect}
	}
	pr, err := h.candidatePR(ctx, r)
	explainPullRequest(&out, pr)
	return out, err
}

// qaHold reads the QA hold of an issue in QAEvaluation, nil when QA did
// not hold its current attempt or no reader is wired.
func (h handlers) qaHold(ctx context.Context, r core.StateRecord) (*QAHold, error) {
	if h.QAHolds == nil || r.State != core.StateQAEvaluation || r.RunID == "" {
		return nil, nil
	}
	hold, err := h.QAHolds.QAHold(ctx, r.RunID, r.Attempt)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return &hold, nil
}

func approvalSteps(pending string, inspect core.NextAction) []core.NextAction {
	if pending == "" {
		return []core.NextAction{{Action: "approvals.list", Description: "find the approval request of this issue"}, inspect}
	}
	id := map[string]string{"id": pending}
	return []core.NextAction{
		{Action: "approvals.get", Description: "read the proposal, its evidence and diff", Params: id},
		{Action: "approvals.decide", Description: "the Owner approves or rejects it", Params: map[string]string{"request_id": pending}},
	}
}

// runEvidence cites the run record and each failed attempt.
func runEvidence(run dispatcher.RunRecord) []Evidence {
	out := []Evidence{{Kind: "run_record", Ref: "run:" + string(run.RunID), At: run.UpdatedAt,
		Summary: fmt.Sprintf("run %s, %d attempt(s) started", run.Status, run.Attempt)}}
	for _, f := range run.Failures {
		summary := fmt.Sprintf("attempt %d failed with %s", f.Attempt, f.Code)
		if f.Detail != "" {
			summary += ": " + f.Detail
		}
		out = append(out, Evidence{Kind: "attempt_failure", Ref: fmt.Sprintf("run:%s/attempt:%d", run.RunID, f.Attempt), Summary: summary, At: f.At})
	}
	return out
}

// approvalEvidence cites a request and, once decided, its decision.
func (h handlers) approvalEvidence(ctx context.Context, r approvals.Request) ([]Evidence, error) {
	out := []Evidence{{Kind: "approval_request", Ref: "approval:" + r.ID, At: r.CreatedAt,
		Summary: fmt.Sprintf("%s request for %s: %s", r.Status, r.Action, r.RiskSummary)}}
	if r.Status == approvals.StatusPending || r.Status == approvals.StatusExpired {
		return out, nil
	}
	a, err := h.Approvals.Decision(ctx, r.ID)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return out, nil
	case err != nil:
		return nil, err
	}
	summary := fmt.Sprintf("%s by %s", a.Decision, a.ActorID)
	if a.Reason != "" {
		summary += ": " + a.Reason
	}
	return append(out, Evidence{Kind: "approval_decision", Ref: "approval:" + r.ID, Summary: summary, At: a.DecidedAt}), nil
}

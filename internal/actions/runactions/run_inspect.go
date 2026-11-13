package runactions

import (
	"context"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
)

// targetInput names the issue or the run a run action is about.
type targetInput struct {
	Issue string     `json:"issue,omitempty"`
	RunID core.RunID `json:"run_id,omitempty"`
}

// Inspection is what runs.inspect returns: an issue's record and the
// dispatcher record of its current run, nil when no run was claimed or the
// dispatcher holds no record of it. The dispatcher's record ends when the
// coder finishes, so a run whose candidate QA held reads as succeeded
// there; QAHold says the issue waits in QAEvaluation for the Owner.
type Inspection struct {
	Record core.StateRecord      `json:"record"`
	Run    *dispatcher.RunRecord `json:"run"`
	QAHold *QAHold               `json:"qa_hold,omitempty"`
	// PullRequest is the pull request of the run's candidate QA passed.
	PullRequest *CandidatePR `json:"pull_request,omitempty"`
	// ReleaseHold is the held or refused release of the issue's candidate
	// while it waits in ReleaseCandidate.
	ReleaseHold *ReleaseHold `json:"release_hold,omitempty"`
}

func (h handlers) runsInspect(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	target, err := h.resolve(ctx, req)
	if err != nil {
		return core.ActionResult{}, err
	}
	if target.QAHold, err = h.qaHold(ctx, target.Record); err != nil {
		return core.ActionResult{}, err
	}
	if target.PullRequest, err = h.candidatePR(ctx, target.Record); err != nil {
		return core.ActionResult{}, err
	}
	if target.ReleaseHold, err = h.releaseHold(ctx, target.Record, req.Actor.HasRole(core.RoleOwner)); err != nil {
		return core.ActionResult{}, err
	}
	return actions.JSONResult(target)
}

// resolve reads the issue or run the input names. A run is found through
// the dispatcher's record, which names its issue; a run that is no longer
// the issue's current run is reported as CONFLICT, since its issue has moved
// on and the caller should look at the issue instead.
func (h handlers) resolve(ctx context.Context, req core.ActionRequest) (Inspection, error) {
	in, err := actions.DecodeInput[targetInput](req)
	if err != nil {
		return Inspection{}, err
	}
	return h.resolveTarget(ctx, in)
}

// resolveTarget reads the issue or run in names, as resolve does.
func (h handlers) resolveTarget(ctx context.Context, in targetInput) (Inspection, error) {
	var err error
	if (in.Issue == "") == (in.RunID == "") {
		return Inspection{}, core.NewError(core.CodeInvalidArgument, "name exactly one of issue and run_id")
	}
	var run *dispatcher.RunRecord
	var issue core.IssueRef
	if in.RunID != "" {
		if err := in.RunID.Validate(); err != nil {
			return Inspection{}, err
		}
		found, err := h.Runs.RunRecord(ctx, in.RunID)
		if err != nil {
			if core.HasCode(err, core.CodeNotFound) {
				return Inspection{}, core.NewError(core.CodeNotFound, "the dispatcher holds no record of this run",
					core.NextAction{Action: ActionIssuesList, Description: "find the issue and ask about it instead"})
			}
			return Inspection{}, err
		}
		run, issue = &found, found.Issue
	} else if issue, err = core.ParseIssueRef(in.Issue); err != nil {
		return Inspection{}, err
	}
	record, err := h.Records.Record(ctx, issue)
	if err != nil {
		if core.HasCode(err, core.CodeNotFound) {
			return Inspection{}, core.NewError(core.CodeNotFound, "no issue with this reference is tracked",
				core.NextAction{Action: ActionIssuesList, Description: "list the tracked issues to find a valid reference"})
		}
		return Inspection{}, err
	}
	if run != nil && record.RunID != run.RunID {
		return Inspection{}, core.NewError(core.CodeConflict, "the run is no longer the current run of its issue",
			core.NextAction{Action: ActionRunsExplain, Description: "ask about the issue instead",
				Params: map[string]string{"issue": issue.String()}})
	}
	if run == nil && record.RunID != "" {
		found, err := h.Runs.RunRecord(ctx, record.RunID)
		switch {
		case err == nil:
			run = &found
		case !core.HasCode(err, core.CodeNotFound):
			return Inspection{}, err
		}
	}
	return Inspection{Record: record, Run: run}, nil
}

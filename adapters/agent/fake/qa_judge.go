package fake

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// scoreReportName is the artifact an evaluator run scripted with Score leaves.
const scoreReportName = "qa-scores.json"

var _ qa.Judge = (*Backend)(nil)

// Score judges a candidate as the evaluator role: it follows the next step
// scripted for the evaluator on the candidate's issue. A Score step returns
// its points; a failed or crashed judgment is UNAVAILABLE and a hanging one
// TIMEOUT, so the evaluator issues no receipt. With no step scripted the
// candidate gets full marks. A backend that does not serve the evaluator
// role answers NOT_SUPPORTED.
func (b *Backend) Score(_ context.Context, req qa.JudgeRequest) (qa.Scorecard, error) {
	if !slices.Contains(b.roles, agent.RoleQA) {
		return qa.Scorecard{}, core.NewError(core.CodeNotSupported, "the fake backend does not serve the qa role")
	}
	if err := validJudgeRequest(req); err != nil {
		return qa.Scorecard{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.judged = append(b.judged, req)
	key := scriptKey{agent.RoleQA, req.Candidate.IssueID}
	queue := b.scripts[key]
	if len(queue) == 0 {
		return qa.Scorecard{Points: maps.Clone(req.Rubric.Weights), EvidenceRef: b.judgmentRef()}, nil
	}
	step := queue[0]
	b.scripts[key] = queue[1:]
	switch step.Outcome {
	case OutcomeScore:
		card := qa.Scorecard{Points: maps.Clone(step.Points), EvidenceRef: b.judgmentRef()}
		if err := card.Validate(req.Rubric); err != nil {
			return qa.Scorecard{}, err
		}
		return card, nil
	case OutcomeFail:
		return qa.Scorecard{}, core.NewError(core.CodeUnavailable, "the evaluator failed: "+step.Detail)
	case OutcomeHang:
		return qa.Scorecard{}, core.NewError(core.CodeTimeout, "the evaluator did not finish")
	case OutcomeCrash:
		return qa.Scorecard{}, core.NewError(core.CodeUnavailable, "the evaluator process exited without a report")
	}
	return qa.Scorecard{}, core.NewError(core.CodeInvalidArgument, "a patch was scripted where a judgment was expected")
}

// Judged returns the judgments asked for issue, oldest first.
func (b *Backend) Judged(issue core.IssueRef) []qa.JudgeRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []qa.JudgeRequest
	for _, req := range b.judged {
		if req.Candidate.IssueID == issue {
			out = append(out, req)
		}
	}
	return out
}

// validJudgeRequest refuses a judgment without a valid candidate, baseline,
// rubric, route or evaluator identity.
func validJudgeRequest(req qa.JudgeRequest) error {
	for _, check := range []func() error{req.Candidate.Validate, req.Baseline.Validate, req.Rubric.Validate, req.ModelRoute.Validate} {
		if err := check(); err != nil {
			return err
		}
	}
	if strings.TrimSpace(req.Identity) == "" {
		return core.NewError(core.CodeInvalidArgument, "a judgment needs the evaluator's identity")
	}
	return nil
}

// judgmentRef names the evidence of the latest judgment. Callers hold b.mu.
func (b *Backend) judgmentRef() string {
	return "fake://" + b.name + "/judgment-" + strconv.Itoa(len(b.judged)) + "/" + scoreReportName
}

// scoreReport is the content of the test report a Score run leaves.
func scoreReport(points map[qa.Category]int) []byte {
	report, _ := json.Marshal(points) // a map of strings to ints always encodes
	return report
}

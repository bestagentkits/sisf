package statemachine

import (
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// stepUpApproval marks the request as one that needs the approver to confirm
// their identity, and has the approver do so between the request and the
// decision.
func stepUpApproval(p *ApprovalProof) {
	p.Request.StepUp = true
	p.Approval.StepUpAt = p.Approval.DecidedAt.Add(-time.Minute)
}

func TestAStepUpApprovalMakesAnIssueReadyWithAConfirmationMadeForIt(t *testing.T) {
	t.Parallel()
	if err := checkApprovalWith(stepUpApproval); err != nil {
		t.Fatalf("an approval confirmed by step-up after its request was made was refused: %v", err)
	}
	atTheDecision := func(p *ApprovalProof) {
		stepUpApproval(p)
		p.Approval.StepUpAt = p.Approval.DecidedAt
	}
	if err := checkApprovalWith(atTheDecision); err != nil {
		t.Fatalf("an approval confirmed at the instant of the decision was refused: %v", err)
	}
}

func TestAStepUpApprovalWithoutAConfirmationDoesNotMakeAnIssueReady(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*ApprovalProof){
		"the approver never confirmed their identity": func(p *ApprovalProof) {
			stepUpApproval(p)
			p.Approval.StepUpAt = time.Time{}
		},
		"the confirmation predates the request": func(p *ApprovalProof) {
			stepUpApproval(p)
			p.Approval.StepUpAt = p.Request.CreatedAt.Add(-time.Nanosecond)
		},
		"the confirmation is dated after the decision": func(p *ApprovalProof) {
			stepUpApproval(p)
			p.Approval.StepUpAt = p.Approval.DecidedAt.Add(time.Nanosecond)
		},
		"the decision came through WebMCP, where an agent in the page acts": func(p *ApprovalProof) {
			stepUpApproval(p)
			p.Approval.Surface = core.SurfaceWebMCP
		},
		"the decision came through WebMCP without any confirmation": func(p *ApprovalProof) {
			stepUpApproval(p)
			p.Approval.Surface, p.Approval.StepUpAt = core.SurfaceWebMCP, time.Time{}
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkApprovalWith(mutate), GuardApproval)
	}
}

func TestAStepUpApprovalCannotLiftADenialWithoutAConfirmation(t *testing.T) {
	t.Parallel()
	err := checkRetryProof(retryAfterDenial, func(p *RetryProof) {
		stepUpApproval(p.Approval)
		p.Approval.Approval.StepUpAt = time.Time{}
	})
	requireGuardFailed(t, "retry on an unconfirmed step-up approval", err, GuardRetry)
}

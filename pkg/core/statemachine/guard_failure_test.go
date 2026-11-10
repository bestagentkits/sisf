package statemachine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

func TestATypedRefusalFromAGuardKeepsItsCode(t *testing.T) {
	overBudget := core.NewError(core.CodeBudgetExceeded, "the monthly cap would be crossed")
	guards := passingGuards()
	guards.Claim = ClaimGuardFunc(func(context.Context, core.Transition) (ClaimProof, error) {
		return ClaimProof{}, overBudget
	})
	err := machineWith(guards).Check(context.Background(), claimRequest())
	if !core.HasCode(err, core.CodeBudgetExceeded) {
		t.Fatalf("want the guard's own %s, got %v", core.CodeBudgetExceeded, err)
	}
}

func TestAGuardThatCannotAnswerRefusesTheStep(t *testing.T) {
	down := errors.New("receipt store unreachable at 10.0.0.5")
	failing := Guards{
		Delegation: DelegationGuardFunc(func(_ context.Context, t core.Transition) (DelegationProof, error) {
			return validDelegation(t), down
		}),
		Approval: ApprovalGuardFunc(func(_ context.Context, t core.Transition) (ApprovalProof, error) {
			return validApproval(t), down
		}),
		Claim:      ClaimGuardFunc(func(context.Context, core.Transition) (ClaimProof, error) { return validClaim(claimRequest()), down }),
		Attempts:   AttemptsGuardFunc(func(_ context.Context, t core.Transition) (AttemptsProof, error) { return validAttempts(t), down }),
		QA:         QAGuardFunc(func(context.Context, core.Transition) (QAProof, error) { return validQA(), down }),
		PostDeploy: PostDeployGuardFunc(func(context.Context, core.Transition) (PostDeployProof, error) { return validPostDeploy(), down }),
		Retry:      RetryGuardFunc(func(context.Context, core.Transition) (core.AuthzDecision, error) { return allowed(), down }),
	}
	cases := guardedRequests()
	for guard, request := range cases {
		err := machineWith(failing).Check(context.Background(), request)
		requireGuardFailed(t, string(guard), err, guard)
		if !errors.Is(err, down) {
			t.Errorf("%s: the underlying failure is lost to operators: %v", guard, err)
		}
		if strings.Contains(err.Error(), "10.0.0.5") {
			t.Errorf("%s: the caller-facing message leaks the internal failure: %q", guard, err.Error())
		}
	}
}

func TestATypedRefusalFromEachNewGuardKeepsItsCode(t *testing.T) {
	limit := core.NewError(core.CodeLimitExceeded, "the run used every attempt")
	needsOwner := core.NewError(core.CodeApprovalRequired, "the scope adds a feature")
	replayed := core.NewError(core.CodeReplayed, "the approval was already used")
	guards := Guards{
		Attempts: AttemptsGuardFunc(func(context.Context, core.Transition) (AttemptsProof, error) {
			return AttemptsProof{}, limit
		}),
		Delegation: DelegationGuardFunc(func(context.Context, core.Transition) (DelegationProof, error) {
			return DelegationProof{}, needsOwner
		}),
		Approval: ApprovalGuardFunc(func(context.Context, core.Transition) (ApprovalProof, error) {
			return ApprovalProof{}, replayed
		}),
	}
	machine := machineWith(guards)
	cases := map[core.ErrorCode]core.Transition{
		core.CodeLimitExceeded:    qaRetryRequest(),
		core.CodeApprovalRequired: delegatedScopeRequest(),
		core.CodeReplayed:         ownerApprovedRequest(),
	}
	for code, request := range cases {
		if err := machine.Check(context.Background(), request); !core.HasCode(err, code) {
			t.Errorf("on %s: want the guard's own %s, got %v", request.Event, code, err)
		}
	}
}

func TestARuleFailureWithoutATypedRefusalStaysInternal(t *testing.T) {
	internal := errors.New("rule table missing at /etc/sisf/rules")
	err := refuseAs(GuardQAPass, qaPassRequest(), internal)
	requireGuardFailed(t, "untyped rule failure", err, GuardQAPass)
	if !errors.Is(err, internal) {
		t.Errorf("the underlying failure is lost to operators: %v", err)
	}
	if strings.Contains(err.Error(), "/etc/sisf") {
		t.Errorf("the caller-facing message leaks the internal failure: %q", err.Error())
	}
}

func TestAStepBehindAnUnknownGuardIsRefused(t *testing.T) {
	// The table only uses known guards; should one ever be added without its
	// rule, the step must stay shut instead of passing unchecked.
	const mystery GuardKind = "mystery"
	err := machineWith(passingGuards()).checkGuard(context.Background(), mystery, claimRequest())
	requireGuardFailed(t, "unknown guard", err, mystery)
}

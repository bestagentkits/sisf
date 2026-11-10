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
		Claim:      ClaimGuardFunc(func(context.Context, core.Transition) (ClaimProof, error) { return validClaim(claimRequest()), down }),
		QA:         QAGuardFunc(func(context.Context, core.Transition) (QAProof, error) { return validQA(), down }),
		PostDeploy: PostDeployGuardFunc(func(context.Context, core.Transition) (PostDeployProof, error) { return validPostDeploy(), down }),
		Retry:      RetryGuardFunc(func(context.Context, core.Transition) (core.AuthzDecision, error) { return allowed(), down }),
	}
	cases := map[GuardKind]core.Transition{
		GuardClaim:      claimRequest(),
		GuardQAPass:     qaPassRequest(),
		GuardPostDeploy: postDeployRequest(),
		GuardRetry:      retryRequest(),
	}
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

func TestAStepBehindAnUnknownGuardIsRefused(t *testing.T) {
	// The table only uses known guards; should one ever be added without its
	// rule, the step must stay shut instead of passing unchecked.
	const mystery GuardKind = "mystery"
	err := machineWith(passingGuards()).checkGuard(context.Background(), mystery, claimRequest())
	requireGuardFailed(t, "unknown guard", err, mystery)
}

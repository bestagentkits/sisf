package statemachine

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// validClaim is the proof of a live lease and a held reservation for t.
func validClaim(t core.Transition) ClaimProof {
	return ClaimProof{
		LeaseOwner:     t.LeaseOwner,
		FencingToken:   t.FencingToken,
		LeaseExpiresAt: testNow.Add(time.Minute),
		Reservation: core.Reservation{
			ID:             "res-0001",
			FactoryID:      "acme-factory",
			IssueID:        t.IssueID,
			RunID:          t.RunID,
			Mode:           core.BudgetUnlimited,
			Status:         core.ReservationHeld,
			IdempotencyKey: "claim:" + string(t.RunID),
			CreatedAt:      testNow,
		},
	}
}

// validQA is the proof of a complete, unexpired receipt that passes both
// required hard gates and clears the pass score.
func validQA() QAProof {
	return QAProof{
		Receipt: core.QAReceipt{
			CandidateSHA:        testSHA,
			CandidateDigest:     testDigest,
			SuiteDigest:         testDigest,
			BaselineSHA:         testSHA,
			PolicySHA:           testSHA,
			QAAgentIdentity:     "agent:qa-1",
			CodingAgentIdentity: "agent:coder-1",
			CodingModelRoute:    core.ModelRoute{Provider: "anthropic", Model: "mid"},
			QAModelRoute:        core.ModelRoute{Provider: "openai", Model: "mid"},
			Checks: []core.QACheck{
				{Name: "tests_contract", Kind: core.CheckHardGate, Passed: true},
				{Name: "rollback_readiness", Kind: core.CheckHardGate, Passed: true},
				{Name: "correctness_regression", Kind: core.CheckRubric, Passed: true, Points: 28, MaxPoints: 30},
			},
			Score:            90,
			IssuedAt:         testNow.Add(-10 * time.Minute),
			ExpiresAt:        testNow.Add(50 * time.Minute),
			EvaluatorAttempt: 1,
			Signature:        core.Signature{KeyID: "qa-2026-11", Algorithm: "ed25519", Value: []byte{1}},
		},
		RequiredGates: []string{"tests_contract", "rollback_readiness"},
		PassScore:     85,
	}
}

// validPostDeploy is the proof that the released build was seen running and
// healthy after its observation window closed, with every DoD item met.
func validPostDeploy() PostDeployProof {
	return PostDeployProof{
		Released:     released,
		Observed:     released,
		Healthy:      true,
		WindowEndsAt: testNow.Add(-time.Minute),
		ObservedAt:   testNow.Add(-30 * time.Second),
		DoD: []DoDResult{
			{Item: "login page renders", Met: true, EvidenceRef: "artifact:smoke-1"},
			{Item: "error rate within baseline", Met: true, EvidenceRef: "artifact:metrics-1"},
		},
	}
}

func allowed() core.AuthzDecision { return core.AuthzDecision{Effect: core.EffectAllow} }

// Guard stand-ins that answer with a fixed proof.
func claimWith(proof func(core.Transition) ClaimProof) ClaimGuardFunc {
	return func(_ context.Context, t core.Transition) (ClaimProof, error) { return proof(t), nil }
}

func qaWith(proof QAProof) QAGuardFunc {
	return func(context.Context, core.Transition) (QAProof, error) { return proof, nil }
}

func postDeployWith(proof PostDeployProof) PostDeployGuardFunc {
	return func(context.Context, core.Transition) (PostDeployProof, error) { return proof, nil }
}

func retryWith(decision core.AuthzDecision) RetryGuardFunc {
	return func(context.Context, core.Transition) (core.AuthzDecision, error) { return decision, nil }
}

// passingGuards answers every guarded step with a valid proof.
func passingGuards() Guards {
	return Guards{
		Claim:      claimWith(validClaim),
		QA:         qaWith(validQA()),
		PostDeploy: postDeployWith(validPostDeploy()),
		Retry:      retryWith(allowed()),
	}
}

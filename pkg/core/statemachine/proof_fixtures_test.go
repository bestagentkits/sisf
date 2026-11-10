package statemachine

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
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

// validQA is the proof of a complete, unexpired receipt for the test issue,
// run and candidate, in which every required hard gate passed and the five
// rubric categories add up to a score that clears the pass score.
func validQA() QAProof {
	var checks []core.QACheck
	for _, gate := range core.RequiredHardGates() {
		checks = append(checks, core.QACheck{Name: gate, Kind: core.CheckHardGate, Passed: true})
	}
	checks = append(checks,
		core.QACheck{Name: "correctness_regression", Kind: core.CheckRubric, Passed: true, Points: 28, MaxPoints: 30},
		core.QACheck{Name: "security_auth", Kind: core.CheckRubric, Passed: true, Points: 23, MaxPoints: 25},
		core.QACheck{Name: "contract_data_safety", Kind: core.CheckRubric, Passed: true, Points: 18, MaxPoints: 20},
		core.QACheck{Name: "ux_performance_output", Kind: core.CheckRubric, Passed: true, Points: 12, MaxPoints: 15},
		core.QACheck{Name: "observability_rollback", Kind: core.CheckRubric, Passed: true, Points: 9, MaxPoints: 10},
	)
	return QAProof{
		Receipt: core.QAReceipt{
			IssueID:             testIssue,
			RunID:               testRun,
			CandidateSHA:        testSHA,
			CandidateDigest:     testDigest,
			SuiteDigest:         testDigest,
			BaselineSHA:         testSHA,
			PolicySHA:           testSHA,
			QAAgentIdentity:     "agent:qa-1",
			CodingAgentIdentity: "agent:coder-1",
			CodingModelRoute:    core.ModelRoute{Provider: "anthropic", Model: "mid"},
			QAModelRoute:        core.ModelRoute{Provider: "openai", Model: "mid"},
			Checks:              checks,
			Score:               90,
			IssuedAt:            testNow.Add(-10 * time.Minute),
			ExpiresAt:           testNow.Add(50 * time.Minute),
			EvaluatorAttempt:    1,
			Signature:           core.Signature{KeyID: "qa-2026-11", Algorithm: "ed25519", Value: []byte{1}},
		},
		RequiredGates: core.RequiredHardGates(),
		PassScore:     85,
	}
}

// scored sets the receipt's score and records all of it in the first rubric
// category, so the score still equals the points recorded.
func scored(p *QAProof, score int) {
	first := true
	for i := range p.Receipt.Checks {
		check := &p.Receipt.Checks[i]
		if check.Kind != core.CheckRubric {
			continue
		}
		check.Points = 0
		if first {
			check.Points, check.MaxPoints = score, max(check.MaxPoints, score)
			first = false
		}
	}
	p.Receipt.Score = score
}

// gateCheck returns the hard-gate result of the named gate.
func gateCheck(p *QAProof, gate string) *core.QACheck {
	for i := range p.Receipt.Checks {
		if p.Receipt.Checks[i].Kind == core.CheckHardGate && p.Receipt.Checks[i].Name == gate {
			return &p.Receipt.Checks[i]
		}
	}
	panic("the receipt fixture records no hard gate " + gate)
}

// validPostDeploy is the proof that the released build was seen running and
// healthy after its observation window closed, with every DoD item met.
func validPostDeploy() PostDeployProof {
	return PostDeployProof{
		IssueID:      testIssue,
		RunID:        testRun,
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

// validDelegation is the proof that policy lets the scope of t's issue
// proceed without the Owner.
func validDelegation(t core.Transition) DelegationProof {
	return DelegationProof{
		IssueID:        t.IssueID,
		ScopeDigest:    scopeDigest,
		PolicyRevision: testSHA,
		Decision:       allowed(),
	}
}

// validApproval is the proof of a signed, unexpired, unused approval by a
// person for the scope of t's issue under the policy in force.
func validApproval(t core.Transition) ApprovalProof {
	request := approvals.Request{
		ID:             "apr-0001",
		FactoryID:      "acme-factory",
		IssueID:        t.IssueID,
		Action:         "scope.approve",
		SubjectDigest:  scopeDigest,
		PolicyRevision: testSHA,
		Nonce:          "b3f1c2d4e5a60718",
		ExpiresAt:      testNow.Add(23 * time.Hour),
		RequestedBy:    "service:dispatcher",
		RiskSummary:    "Adds one public endpoint.",
		Status:         approvals.StatusApproved,
		CreatedAt:      testNow.Add(-time.Hour),
	}
	return ApprovalProof{
		Request: request,
		Approval: approvals.Approval{
			RequestID:      request.ID,
			SubjectDigest:  request.SubjectDigest,
			PolicyRevision: request.PolicyRevision,
			Nonce:          request.Nonce,
			Decision:       approvals.DecisionApprove,
			ActorID:        "user:owner-1",
			ActorKind:      core.ActorHuman,
			Surface:        core.SurfaceDashboard,
			DecidedAt:      testNow.Add(-30 * time.Minute),
			ExpiresAt:      testNow.Add(time.Hour),
			Signature:      core.Signature{KeyID: "approvals-2026-11", Algorithm: "ed25519", Value: []byte{1}},
		},
		ScopeDigest:    scopeDigest,
		PolicyRevision: testSHA,
	}
}

// validAttempts is the proof that the run of t has used the attempt before
// the one t starts, with the limit of the test policy.
func validAttempts(t core.Transition) AttemptsProof {
	return AttemptsProof{
		IssueID:     t.IssueID,
		RunID:       t.RunID,
		Attempt:     t.Attempt - 1,
		MaxAttempts: testMaxAttempts,
	}
}

func allowed() core.AuthzDecision { return core.AuthzDecision{Effect: core.EffectAllow} }

// Guard stand-ins that answer with a fixed proof.
func delegationWith(proof func(core.Transition) DelegationProof) DelegationGuardFunc {
	return func(_ context.Context, t core.Transition) (DelegationProof, error) { return proof(t), nil }
}

func approvalWith(proof func(core.Transition) ApprovalProof) ApprovalGuardFunc {
	return func(_ context.Context, t core.Transition) (ApprovalProof, error) { return proof(t), nil }
}

func claimWith(proof func(core.Transition) ClaimProof) ClaimGuardFunc {
	return func(_ context.Context, t core.Transition) (ClaimProof, error) { return proof(t), nil }
}

func attemptsWith(proof func(core.Transition) AttemptsProof) AttemptsGuardFunc {
	return func(_ context.Context, t core.Transition) (AttemptsProof, error) { return proof(t), nil }
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
		Delegation: delegationWith(validDelegation),
		Approval:   approvalWith(validApproval),
		Claim:      claimWith(validClaim),
		Attempts:   attemptsWith(validAttempts),
		QA:         qaWith(validQA()),
		PostDeploy: postDeployWith(validPostDeploy()),
		Retry:      retryWith(allowed()),
	}
}

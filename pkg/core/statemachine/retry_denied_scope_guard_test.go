package statemachine

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// retryOnDelegationAfterRejection is the proof of an authorized retry of an
// issue the Owner refused, resting on the policy's answer that the scope now
// lies within the delegation. The refused request was about the same scope
// under an older policy revision.
func retryOnDelegationAfterRejection(t core.Transition) RetryProof {
	proof := validRetry(t)
	proof.Cause = BlockApprovalDenied
	proof.DeniedScope, proof.DeniedPolicy = scopeDigest, otherSHA
	delegation := validDelegation(t)
	proof.Delegation = &delegation
	return proof
}

func TestADelegationLiftsARefusalOnlyForAnotherScopeOrPolicy(t *testing.T) {
	t.Parallel()
	if err := checkRetryProof(retryOnDelegationAfterRejection, func(*RetryProof) {}); err != nil {
		t.Fatalf("a refused scope that a later policy revision delegates was not retried: %v", err)
	}
	edited := func(p *RetryProof) { p.DeniedScope, p.DeniedPolicy = testDigest, testSHA }
	if err := checkRetryProof(retryOnDelegationAfterRejection, edited); err != nil {
		t.Fatalf("a scope edited since the refusal and now delegated was not retried: %v", err)
	}
}

func TestADelegationDoesNotUndoTheOwnersRefusalOfTheSameScope(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*RetryProof){
		"the refused scope under the policy it was refused under": func(p *RetryProof) {
			p.DeniedScope, p.DeniedPolicy = scopeDigest, testSHA
		},
		"it is not known which scope was refused":      func(p *RetryProof) { p.DeniedScope = "" },
		"it is not known under which policy":           func(p *RetryProof) { p.DeniedPolicy = "" },
		"nothing is known of the refused request":      func(p *RetryProof) { p.DeniedScope, p.DeniedPolicy = "", "" },
		"the refused scope is not a digest":            func(p *RetryProof) { p.DeniedScope = "sow-v1" },
		"the refused policy is not pinned to a commit": func(p *RetryProof) { p.DeniedPolicy = "main" },
	}
	for name, mutate := range cases {
		err := checkRetryProof(retryOnDelegationAfterRejection, mutate)
		requireGuardFailed(t, name, err, GuardRetry)
		if got := detailsOf(err)["cause"]; got != string(BlockApprovalDenied) {
			t.Errorf("%s: refusal reports cause %q", name, got)
		}
	}
}

func TestAnApprovalAfterTheRefusalLiftsItForTheSameScope(t *testing.T) {
	t.Parallel()
	// The Owner who refused a scope may approve the same scope later; that
	// decision, not the policy, is what lifts the refusal.
	err := checkRetryProof(retryAfterDenial, func(p *RetryProof) { p.DeniedScope, p.DeniedPolicy = scopeDigest, testSHA })
	if err != nil {
		t.Fatalf("an approval the Owner gave after refusing the same scope was refused: %v", err)
	}
}

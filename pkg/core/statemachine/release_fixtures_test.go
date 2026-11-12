package statemachine

import (
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// validRollout is the proof of a rollback preflight for the test release: a
// signed artifact of the test factory whose digest is the release, a verified
// earlier revision of the same unit to roll back to, and readable metrics.
func validRollout() RolloutProof {
	return RolloutProof{
		IssueID:   testIssue,
		RunID:     testRun,
		FactoryID: testFactory,
		Artifact: deploy.Artifact{
			FactoryID: testFactory,
			Unit:      "app",
			Digest:    released,
			Image:     "ghcr.io/acme/app@" + string(released),
			Signature: core.Signature{KeyID: "release-2026-11", Algorithm: "ed25519", Value: []byte{1}},
		},
		RollbackTarget:   deploy.Revision{Unit: "app", Digest: otherBuild, ID: "blue"},
		RollbackVerified: true,
		MetricsAvailable: true,
		SelfPublishable:  []string{"app", "control", "dispatcher", "runner"},
	}
}

// validCanary is the proof that the released build was seen running and
// healthy during its rollout, with its metrics readable.
func validCanary() CanaryProof {
	return CanaryProof{
		IssueID:          testIssue,
		RunID:            testRun,
		Released:         released,
		Observed:         released,
		MetricsAvailable: true,
		Healthy:          true,
		ObservedAt:       testNow.Add(-time.Minute),
		RolloutStartedAt: testNow.Add(-10 * time.Minute),
	}
}

// blockedAt is when the test issue was blocked.
var blockedAt = testNow.Add(-2 * time.Hour)

// validRetry is the proof of an authorized retry, by the person t names, of
// an issue that is blocked because its release was rolled back.
func validRetry(t core.Transition) RetryProof {
	return RetryProof{
		IssueID:   t.IssueID,
		Cause:     BlockRolledBack,
		BlockedAt: blockedAt,
		Actor:     core.Actor{ID: t.ActorID, Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}},
		Decision:  allowed(),
	}
}

// retryAfterDenial is the proof of an authorized retry of an issue whose
// approval was denied, together with an approval the Owner gave after the
// block.
func retryAfterDenial(t core.Transition) RetryProof {
	proof := validRetry(t)
	proof.Cause = BlockApprovalDenied
	approval := validApproval(t)
	proof.Approval = &approval
	return proof
}

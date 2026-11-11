package statemachine

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// A candidate that passed QA reaches users in two steps: the rollout starts
// once the rollback preflight holds, and the release is left to its
// observation window once it was seen running and healthy. The guards below
// supply the facts of each. A release that cannot be observed or rolled back
// holds; success is never assumed.

// RolloutProof is the result of the rollback preflight of one release.
type RolloutProof struct {
	// IssueID and RunID name the issue and the run the release is made for.
	// They must be those of the transition.
	IssueID core.IssueRef
	RunID   core.RunID
	// FactoryID is the factory deployment this machine serves. The artifact
	// must have been built and signed for it.
	FactoryID core.FactoryID
	// Artifact is the signed build about to take traffic. Its digest must be
	// the release the transition names, and that release must be the
	// candidate QA passed: what is rolled out is what was judged.
	Artifact deploy.Artifact
	// RollbackTarget is the revision the unit returns to when the rollout
	// fails. It must be a revision of the artifact's unit and another build
	// than the artifact.
	RollbackTarget deploy.Revision
	// RollbackVerified says the preflight confirmed with the deployment
	// backend that RollbackTarget is still there and that the backend can
	// switch back to it. A target that is only assumed is not verified.
	RollbackVerified bool
	// MetricsAvailable says the metrics and the observed revision of the
	// unit could be read from the backend during the preflight. A release
	// that cannot be observed cannot be judged and does not start.
	MetricsAvailable bool
}

// RolloutGuard proves that a release may start: its artifact is authentic
// and there is a way to observe it and a way back. The implementation must
// return only an artifact whose signature it has verified, over the
// artifact's SigningPayload, against the trusted release keys; the machine
// cannot check that. The machine checks everything else: that the preflight
// is of the issue, the run and the release of the transition, that the
// release is the candidate QA passed and a build of this factory, and that
// the rollback target and the metrics are there. Without a verified way back
// or readable metrics the step is refused with RELEASE_HOLD.
type RolloutGuard interface {
	RolloutProof(ctx context.Context, t core.Transition) (RolloutProof, error)
}

// CanaryProof is what was seen of a release while it was rolled out.
type CanaryProof struct {
	// IssueID and RunID name the issue and the run the release was made for.
	// They must be those of the transition.
	IssueID core.IssueRef
	RunID   core.RunID
	// Released is the digest the rollout set out to run. It must be the
	// release digest the transition names.
	Released core.ReleaseDigest
	// Observed is the digest read from the running system, never taken from
	// the result of the deploy call.
	Observed core.ReleaseDigest
	// MetricsAvailable says the measurements behind Healthy could be read. A
	// rollout whose metrics are missing holds; it is not healthy by default.
	MetricsAvailable bool
	// Healthy says that, at ObservedAt, the health check of the running
	// revision passed and its error rate stayed within the rollback
	// threshold. ObservedAt must not lie after the current instant.
	Healthy    bool
	ObservedAt time.Time
	// RolloutStartedAt is when the store wrote the start of this rollout,
	// taken from the record, never from the backend. ObservedAt must not lie
	// before it: an observation made before the rollout, such as one of an
	// earlier release of the same digest, says nothing about this one.
	RolloutStartedAt time.Time
}

// CanaryGuard proves that a rollout went well: the released revision was
// observed running and healthy. The result of the call that started the
// rollout is not such a proof. Missing metrics refuse the step with
// RELEASE_HOLD; an unhealthy rollout is refused and leaves the way to
// RolledBack.
type CanaryGuard interface {
	CanaryProof(ctx context.Context, t core.Transition) (CanaryProof, error)
}

// RolloutGuardFunc adapts a function to RolloutGuard.
type RolloutGuardFunc func(ctx context.Context, t core.Transition) (RolloutProof, error)

// RolloutProof calls f.
func (f RolloutGuardFunc) RolloutProof(ctx context.Context, t core.Transition) (RolloutProof, error) {
	return f(ctx, t)
}

// CanaryGuardFunc adapts a function to CanaryGuard.
type CanaryGuardFunc func(ctx context.Context, t core.Transition) (CanaryProof, error)

// CanaryProof calls f.
func (f CanaryGuardFunc) CanaryProof(ctx context.Context, t core.Transition) (CanaryProof, error) {
	return f(ctx, t)
}

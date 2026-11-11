package statemachine

import (
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// hold builds the RELEASE_HOLD refusal of a release guard: the proof is about
// the right release, but the release cannot be observed or rolled back, so it
// waits. The details are those of a guard refusal.
func hold(guard GuardKind, t core.Transition, format string, args ...any) *core.Error {
	refusal := refuse(guard, t, format, args...)
	refusal.Code = core.CodeReleaseHold
	return refusal
}

// checkCandidate requires a submission to name its run and the digest of the
// finished build. Code that has not finished building has no digest and
// cannot be handed to evaluation.
func checkCandidate(t core.Transition) error {
	if t.RunID == "" || t.CandidateDigest == "" {
		return refuse(GuardCandidate, t, "a submitted candidate must name its run and the digest of the finished build")
	}
	return nil
}

// checkRollout requires the preflight to be of the issue, the run and the
// release of the transition, the release to be the candidate QA passed and a
// signed build of this factory, and a verified revision to roll back to with
// readable metrics. The last two missing mean hold.
func checkRollout(t core.Transition, p RolloutProof) error {
	artifact, target := p.Artifact, p.RollbackTarget
	switch {
	case t.RunID == "" || t.CandidateDigest == "" || t.ReleaseDigest == "":
		return refuse(GuardRollout, t, "the step must name the run, the candidate and the release it rolls out")
	case t.ReleaseDigest != core.ReleaseDigest(t.CandidateDigest):
		return refuse(GuardRollout, t, "the release is not the candidate that passed QA")
	case p.IssueID != t.IssueID || p.RunID != t.RunID:
		return refuse(GuardRollout, t, "the rollback preflight belongs to another issue or run")
	}
	if err := artifact.ValidateFor(p.FactoryID); err != nil {
		return refuse(GuardRollout, t, "the artifact is not a signed build of this factory: %v", err).WithCause(err)
	}
	switch {
	case artifact.Digest != t.ReleaseDigest:
		return refuse(GuardRollout, t, "the artifact is another build than the release")
	case target.Unit != artifact.Unit || target.Digest.Validate() != nil:
		return hold(GuardRollout, t, "unit %q has no revision to roll back to", artifact.Unit)
	case target.Digest == artifact.Digest:
		return hold(GuardRollout, t, "rolling back to the release itself is no way back")
	case !p.RollbackVerified:
		return hold(GuardRollout, t, "the revision to roll back to was not verified")
	case !p.MetricsAvailable:
		return hold(GuardRollout, t, "the release cannot be observed: its metrics are not available")
	}
	return nil
}

// checkCanary requires the observation to be of the issue, the run and the
// release of the transition, read with its metrics, of the released digest
// running and healthy, and dated within the rollout: no earlier than its
// start and no later than now. Missing metrics mean hold.
func checkCanary(t core.Transition, p CanaryProof, now time.Time) error {
	switch {
	case t.RunID == "" || t.ReleaseDigest == "":
		return refuse(GuardCanaryHealth, t, "the step must name the run and the release that was rolled out")
	case p.IssueID != t.IssueID || p.RunID != t.RunID:
		return refuse(GuardCanaryHealth, t, "the observation belongs to another issue or run")
	case p.Released.Validate() != nil:
		return refuse(GuardCanaryHealth, t, "the release is not identified by a full digest")
	case p.Released != t.ReleaseDigest:
		return refuse(GuardCanaryHealth, t, "the observation is of another release")
	case !p.MetricsAvailable:
		return hold(GuardCanaryHealth, t, "the rollout cannot be judged: its metrics are not available")
	case p.Observed != p.Released:
		return refuse(GuardCanaryHealth, t, "the revision observed running is not the released one")
	case p.ObservedAt.IsZero():
		return refuse(GuardCanaryHealth, t, "the observation is not dated")
	case p.ObservedAt.After(now):
		return refuse(GuardCanaryHealth, t, "the observation is dated after the current instant")
	case p.RolloutStartedAt.IsZero() || p.RolloutStartedAt.After(now):
		return refuse(GuardCanaryHealth, t, "it is not known when the rollout started")
	case p.ObservedAt.Before(p.RolloutStartedAt):
		return refuse(GuardCanaryHealth, t, "the observation was made before the rollout started and says nothing about it")
	}
	if !p.Healthy {
		refusal := refuse(GuardCanaryHealth, t, "the rolled-out revision is not healthy")
		refusal.Details["allowed_event"] = string(core.EventErrorThresholdExceeded)
		return refusal
	}
	return nil
}

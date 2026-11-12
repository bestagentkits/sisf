package statemachine

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// checkRolloutOn runs the step from ReleaseCandidate to Canary after the
// request and the preflight proof have each been changed.
func checkRolloutOn(mutateRequest func(*core.Transition), mutate func(*RolloutProof)) error {
	request := rolloutRequest()
	mutateRequest(&request)
	proof := validRollout()
	mutate(&proof)
	return machineWith(Guards{Rollout: rolloutWith(proof)}).Check(context.Background(), request)
}

func checkRolloutWith(mutate func(*RolloutProof)) error { return checkRolloutOn(unchanged, mutate) }

// checkCanaryOn runs the step from Canary to Monitoring after the request and
// the observation have each been changed.
func checkCanaryOn(mutateRequest func(*core.Transition), mutate func(*CanaryProof)) error {
	request := canaryRequest()
	mutateRequest(&request)
	proof := validCanary()
	mutate(&proof)
	return machineWith(Guards{Canary: canaryWith(proof)}).Check(context.Background(), request)
}

func checkCanaryWith(mutate func(*CanaryProof)) error { return checkCanaryOn(unchanged, mutate) }

func TestACandidateIsSubmittedOnlyWithItsRunAndTheDigestOfItsBuild(t *testing.T) {
	t.Parallel()
	// The proof is in the request, so no guard has to be wired for it.
	machine := machineWith(Guards{})
	if err := machine.Check(context.Background(), candidateRequest()); err != nil {
		t.Fatalf("a submission that names its run and its build was refused: %v", err)
	}
	cases := map[string]func(*core.Transition){
		"the build has no digest yet": func(r *core.Transition) { r.CandidateDigest = "" },
		"the submission names no run": func(r *core.Transition) { r.RunID = "" },
		"neither is named":            func(r *core.Transition) { r.RunID, r.CandidateDigest = "", "" },
	}
	for name, mutate := range cases {
		request := candidateRequest()
		mutate(&request)
		requireGuardFailed(t, name, machine.Check(context.Background(), request), GuardCandidate)
		// Wiring every other guard does not let the bare submission through.
		requireGuardFailed(t, name+", every guard wired",
			machineWith(passingGuards()).Check(context.Background(), request), GuardCandidate)
	}
	named := candidateRequest()
	named.CandidateDigest = "build-42"
	if err := machine.Check(context.Background(), named); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a candidate named by something other than a digest: want %s, got %v", core.CodeInvalidArgument, err)
	}
}

func TestARolloutStartsOnASignedArtifactWithAWayBackAndMetrics(t *testing.T) {
	t.Parallel()
	if err := checkRolloutWith(func(*RolloutProof) {}); err != nil {
		t.Fatalf("a rollout whose preflight holds was refused: %v", err)
	}
}

func TestARolloutCannotStartWithoutItsPreflight(t *testing.T) {
	t.Parallel()
	requireGuardFailed(t, "no rollout guard wired",
		machineWith(Guards{}).Check(context.Background(), rolloutRequest()), GuardRollout)
	requireGuardFailed(t, "empty proof", checkRolloutWith(func(p *RolloutProof) { *p = RolloutProof{} }), GuardRollout)

	// A passing receipt is the proof of the step before; it starts no rollout.
	onlyQA := Guards{QA: qaWith(validQA())}
	requireGuardFailed(t, "QA receipt offered instead of a preflight",
		machineWith(onlyQA).Check(context.Background(), rolloutRequest()), GuardRollout)
}

func TestARolloutOfAnythingButTheSignedCandidateOfThisFactoryIsRefused(t *testing.T) {
	t.Parallel()
	proofs := map[string]func(*RolloutProof){
		"the artifact is unsigned":                   func(p *RolloutProof) { p.Artifact.Signature = core.Signature{} },
		"the signature names no key":                 func(p *RolloutProof) { p.Artifact.Signature.KeyID = "" },
		"the artifact is another build":              func(p *RolloutProof) { p.Artifact.Digest = otherBuild },
		"the artifact is named by a tag":             func(p *RolloutProof) { p.Artifact.Digest = "latest" },
		"the artifact names no unit":                 func(p *RolloutProof) { p.Artifact.Unit = "" },
		"the artifact names no image":                func(p *RolloutProof) { p.Artifact.Image = "" },
		"the artifact was built for another factory": func(p *RolloutProof) { p.Artifact.FactoryID = otherFactory },
		"the artifact names no factory":              func(p *RolloutProof) { p.Artifact.FactoryID = "" },
		"this machine serves another factory":        func(p *RolloutProof) { p.FactoryID = otherFactory },
		"the factory in force is unknown":            func(p *RolloutProof) { p.FactoryID = "" },
		"no factory is named anywhere":               func(p *RolloutProof) { p.FactoryID, p.Artifact.FactoryID = "", "" },
		"the preflight is of another issue":          func(p *RolloutProof) { p.IssueID = otherIssue },
		"the preflight is of another run":            func(p *RolloutProof) { p.RunID = otherRun },
		"no artifact was presented":                  func(p *RolloutProof) { p.Artifact = deploy.Artifact{} },
	}
	for name, mutate := range proofs {
		requireGuardFailed(t, name, checkRolloutWith(mutate), GuardRollout)
	}
	requests := map[string]func(*core.Transition){
		"the step names no run":       func(r *core.Transition) { r.RunID = "" },
		"the step names no candidate": func(r *core.Transition) { r.CandidateDigest = "" },
		"the step names no release":   func(r *core.Transition) { r.ReleaseDigest = "" },
		"the step is for another run": func(r *core.Transition) { r.RunID = otherRun },
		"the release is another build than the candidate QA passed": func(r *core.Transition) {
			r.ReleaseDigest = otherBuild
		},
		"the candidate is another build than the release": func(r *core.Transition) {
			r.CandidateDigest = core.Digest(otherBuild)
		},
	}
	for name, mutate := range requests {
		requireGuardFailed(t, name, checkRolloutOn(mutate, func(*RolloutProof) {}), GuardRollout)
	}
	// Swapping candidate and release together does not help: the artifact
	// that was verified is still another build.
	swapped := checkRolloutOn(func(r *core.Transition) {
		r.CandidateDigest, r.ReleaseDigest = core.Digest(otherBuild), otherBuild
	}, func(*RolloutProof) {})
	requireGuardFailed(t, "candidate and release both name another build", swapped, GuardRollout)
}

func TestAReleaseThatCannotBeRolledBackOrObservedHolds(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*RolloutProof){
		"there is no revision to roll back to":   func(p *RolloutProof) { p.RollbackTarget = deploy.Revision{} },
		"the rollback target is named by a tag":  func(p *RolloutProof) { p.RollbackTarget.Digest = "previous" },
		"the rollback target is of another unit": func(p *RolloutProof) { p.RollbackTarget.Unit = "control" },
		"the rollback target is the release itself": func(p *RolloutProof) {
			p.RollbackTarget.Digest = p.Artifact.Digest
		},
		"the rollback target was not verified": func(p *RolloutProof) { p.RollbackVerified = false },
		"the metrics are not available":        func(p *RolloutProof) { p.MetricsAvailable = false },
		"neither a way back nor metrics":       func(p *RolloutProof) { p.RollbackVerified, p.MetricsAvailable = false, false },
	}
	for name, mutate := range cases {
		requireHold(t, name, checkRolloutWith(mutate), GuardRollout)
	}
}

func TestARolloutIsLeftToMonitoringOnceTheReleaseWasObservedHealthy(t *testing.T) {
	t.Parallel()
	if err := checkCanaryWith(func(*CanaryProof) {}); err != nil {
		t.Fatalf("a rollout observed running and healthy was refused: %v", err)
	}
	if err := checkCanaryWith(func(p *CanaryProof) { p.ObservedAt = testNow }); err != nil {
		t.Fatalf("an observation made at this very instant was refused: %v", err)
	}
}

func TestARolloutIsNotHealthyWithoutAnObservationOfTheRelease(t *testing.T) {
	t.Parallel()
	requireGuardFailed(t, "no canary guard wired",
		machineWith(Guards{}).Check(context.Background(), canaryRequest()), GuardCanaryHealth)
	requireGuardFailed(t, "empty proof", checkCanaryWith(func(p *CanaryProof) { *p = CanaryProof{} }), GuardCanaryHealth)

	proofs := map[string]func(*CanaryProof){
		"the running revision is unhealthy":     func(p *CanaryProof) { p.Healthy = false },
		"another revision is running":           func(p *CanaryProof) { p.Observed = otherBuild },
		"nothing was observed running":          func(p *CanaryProof) { p.Observed = "" },
		"the observation is of another release": func(p *CanaryProof) { p.Released, p.Observed = otherBuild, otherBuild },
		"the release is named by a tag":         func(p *CanaryProof) { p.Released, p.Observed = "v2", "v2" },
		"the observation is of another issue":   func(p *CanaryProof) { p.IssueID = otherIssue },
		"the observation is of another run":     func(p *CanaryProof) { p.RunID = otherRun },
		"the observation is not dated":          func(p *CanaryProof) { p.ObservedAt = time.Time{} },
		"the observation is dated after now":    func(p *CanaryProof) { p.ObservedAt = testNow.Add(time.Nanosecond) },
	}
	for name, mutate := range proofs {
		requireGuardFailed(t, name, checkCanaryWith(mutate), GuardCanaryHealth)
	}
	requests := map[string]func(*core.Transition){
		"the step names no run":           func(r *core.Transition) { r.RunID = "" },
		"the step names no release":       func(r *core.Transition) { r.ReleaseDigest = "" },
		"the step is for another release": func(r *core.Transition) { r.ReleaseDigest = otherBuild },
	}
	for name, mutate := range requests {
		requireGuardFailed(t, name, checkCanaryOn(mutate, func(*CanaryProof) {}), GuardCanaryHealth)
	}
}

func TestARolloutWithoutMetricsHoldsInsteadOfPassingAsHealthy(t *testing.T) {
	t.Parallel()
	requireHold(t, "healthy by the health check alone",
		checkCanaryWith(func(p *CanaryProof) { p.MetricsAvailable = false }), GuardCanaryHealth)
	requireHold(t, "no metrics and unhealthy",
		checkCanaryWith(func(p *CanaryProof) { p.MetricsAvailable, p.Healthy = false, false }), GuardCanaryHealth)
}

func TestAnUnhealthyRolloutIsPointedToTheRollback(t *testing.T) {
	t.Parallel()
	err := checkCanaryWith(func(p *CanaryProof) { p.Healthy = false })
	if got := detailsOf(err)["allowed_event"]; got != string(core.EventErrorThresholdExceeded) {
		t.Fatalf("the refusal points to %q, want the rollback event", got)
	}
	// The rollback itself needs no proof, so a failing release is never stuck.
	back := transitionOn(core.StateCanary, core.EventErrorThresholdExceeded, core.StateRolledBack)
	if err := machineWith(Guards{}).Check(context.Background(), back); err != nil {
		t.Fatalf("rolling a failing release back was refused: %v", err)
	}
}

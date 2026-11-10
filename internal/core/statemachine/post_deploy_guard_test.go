package statemachine

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
)

// checkPostDeployWith runs the step into Verified after mutate has changed
// the proof.
func checkPostDeployWith(mutate func(*PostDeployProof)) error {
	proof := validPostDeploy()
	mutate(&proof)
	return machineWith(Guards{PostDeploy: postDeployWith(proof)}).Check(context.Background(), postDeployRequest())
}

func TestAReleaseIsVerifiedWhenTheObservedRevisionMeetsTheDoDAfterTheWindow(t *testing.T) {
	if err := checkPostDeployWith(func(*PostDeployProof) {}); err != nil {
		t.Fatalf("an observed, healthy release that met its DoD was refused: %v", err)
	}
}

func TestAReleaseIsVerifiedAtTheInstantItsWindowCloses(t *testing.T) {
	err := checkPostDeployWith(func(p *PostDeployProof) {
		p.WindowEndsAt = testNow
		p.ObservedAt = testNow
	})
	if err != nil {
		t.Fatalf("a release observed exactly when its window closed was refused: %v", err)
	}
}

func TestAReleaseThatWasNotObservedRunningIsNotVerified(t *testing.T) {
	cases := map[string]func(*PostDeployProof){
		"no release digest": func(p *PostDeployProof) { p.Released, p.Observed = "", "" },
		"release is a tag, not a digest": func(p *PostDeployProof) {
			p.Released, p.Observed = "v1.2.3", "v1.2.3"
		},
		"nothing was observed":              func(p *PostDeployProof) { p.Observed = "" },
		"another build is running":          func(p *PostDeployProof) { p.Observed = otherBuild },
		"the running revision is unhealthy": func(p *PostDeployProof) { p.Healthy = false },
		"the observation has no instant":    func(p *PostDeployProof) { p.ObservedAt = time.Time{} },
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkPostDeployWith(mutate), GuardPostDeploy)
	}
}

func TestAReleaseIsNotVerifiedBeforeItsObservationWindowHasPassed(t *testing.T) {
	cases := map[string]func(*PostDeployProof){
		"no observation window was set": func(p *PostDeployProof) { p.WindowEndsAt = time.Time{} },
		"the window is still open": func(p *PostDeployProof) {
			p.WindowEndsAt = testNow.Add(time.Nanosecond)
			p.ObservedAt = testNow
		},
		"the window closes in ten minutes": func(p *PostDeployProof) {
			p.WindowEndsAt = testNow.Add(10 * time.Minute)
		},
		"an observation dated ahead of the clock does not close the window early": func(p *PostDeployProof) {
			p.WindowEndsAt = testNow.Add(10 * time.Minute)
			p.ObservedAt = testNow.Add(11 * time.Minute)
		},
		"the revision was last observed before the window closed": func(p *PostDeployProof) {
			p.ObservedAt = p.WindowEndsAt.Add(-time.Nanosecond)
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkPostDeployWith(mutate), GuardPostDeploy)
	}
}

func TestAReleaseThatMissesItsDoDIsNotVerified(t *testing.T) {
	cases := map[string]func(*PostDeployProof){
		"no DoD result was recorded": func(p *PostDeployProof) { p.DoD = nil },
		"one DoD item is not met":    func(p *PostDeployProof) { p.DoD[1].Met = false },
		"no DoD item is met": func(p *PostDeployProof) {
			p.DoD[0].Met, p.DoD[1].Met = false, false
		},
		"a DoD result names no item": func(p *PostDeployProof) { p.DoD[0].Item = " " },
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkPostDeployWith(mutate), GuardPostDeploy)
	}
}

func TestAnUnmetDoDItemIsNamedInTheRefusal(t *testing.T) {
	err := checkPostDeployWith(func(p *PostDeployProof) { p.DoD[1].Met = false })
	if got := detailsOf(err)["dod_item"]; got != "error rate within baseline" {
		t.Fatalf("refusal names DoD item %q, want the unmet one", got)
	}
}

func TestAnEmptyPostDeployProofIsRefused(t *testing.T) {
	guards := Guards{PostDeploy: postDeployWith(PostDeployProof{})}
	err := machineWith(guards).Check(context.Background(), postDeployRequest())
	requireGuardFailed(t, "empty proof", err, GuardPostDeploy)
}

func TestARegressionRollsBackWithoutPostDeployProof(t *testing.T) {
	// Leaving Monitoring for RolledBack must never wait on the proof that
	// verification needs, or a bad release could not be undone.
	request := transitionOn(core.StateMonitoring, core.EventRegressionDetected, core.StateRolledBack)
	if err := machineWith(Guards{}).Check(context.Background(), request); err != nil {
		t.Fatalf("rolling back a regression was refused: %v", err)
	}
}

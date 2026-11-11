package statemachine

import "testing"

// rolloutOfUnit makes the release a build of unit, with a way back of the
// same unit, so only the unit itself can stand in the way.
func rolloutOfUnit(unit string) func(*RolloutProof) {
	return func(p *RolloutProof) { p.Artifact.Unit, p.RollbackTarget.Unit = unit, unit }
}

func TestARolloutOfAUnitTheFactoryMayPublishStarts(t *testing.T) {
	t.Parallel()
	for _, unit := range []string{"app", "control", "dispatcher", "runner"} {
		if err := checkRolloutWith(rolloutOfUnit(unit)); err != nil {
			t.Errorf("a rollout of %s, which release.units lists, was refused: %v", unit, err)
		}
	}
}

func TestARolloutOfAUnitTheFactoryMayNotPublishIsRefused(t *testing.T) {
	t.Parallel()
	for _, unit := range []string{"supervisor", "qa-runner", "edge", "state", "artifacts", "App", "app "} {
		requireGuardFailed(t, "a rollout of "+unit, checkRolloutWith(rolloutOfUnit(unit)), GuardRollout)
	}
	cases := map[string]func(*RolloutProof){
		"release.units does not list the unit": func(p *RolloutProof) { p.SelfPublishable = []string{"control"} },
		"release.units is empty":               func(p *RolloutProof) { p.SelfPublishable = []string{} },
		"release.units is not known":           func(p *RolloutProof) { p.SelfPublishable = nil },
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkRolloutWith(mutate), GuardRollout)
	}
}

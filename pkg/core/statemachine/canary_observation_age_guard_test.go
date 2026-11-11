package statemachine

import (
	"testing"
	"time"
)

func TestAnObservationMadeDuringTheRolloutCounts(t *testing.T) {
	t.Parallel()
	atTheStart := func(p *CanaryProof) { p.ObservedAt = p.RolloutStartedAt }
	if err := checkCanaryWith(atTheStart); err != nil {
		t.Fatalf("an observation made at the instant the rollout started was refused: %v", err)
	}
	startedNow := func(p *CanaryProof) { p.RolloutStartedAt, p.ObservedAt = testNow, testNow }
	if err := checkCanaryWith(startedNow); err != nil {
		t.Fatalf("a rollout started and observed at this instant was refused: %v", err)
	}
}

func TestAnObservationOlderThanTheRolloutDoesNotProveItHealthy(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*CanaryProof){
		"the observation predates the rollout": func(p *CanaryProof) {
			p.ObservedAt = p.RolloutStartedAt.Add(-time.Nanosecond)
		},
		"the observation is of an earlier release of the same digest": func(p *CanaryProof) {
			p.ObservedAt = p.RolloutStartedAt.Add(-24 * time.Hour)
		},
		"it is not known when the rollout started": func(p *CanaryProof) { p.RolloutStartedAt = time.Time{} },
		"the rollout is dated after now": func(p *CanaryProof) {
			p.RolloutStartedAt, p.ObservedAt = testNow.Add(time.Nanosecond), testNow
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkCanaryWith(mutate), GuardCanaryHealth)
	}
}

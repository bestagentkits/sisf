package statemachine

import (
	"context"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// unleased strips the lease from a request, as a worker that lost its lease
// and writes with no token would send it.
func unleased(t core.Transition) core.Transition {
	t.LeaseOwner, t.FencingToken = "", 0
	return t
}

func TestARunHoldsTheIssueFromTheClaimUntilTheReleaseLeavesMonitoring(t *testing.T) {
	t.Parallel()
	held := []core.State{
		core.StateClaimed, core.StateDeveloping, core.StateQAEvaluation,
		core.StateReleaseCandidate, core.StateCanary, core.StateMonitoring,
	}
	for _, state := range core.States() {
		if got, want := RequiresLease(state), slices.Contains(held, state); got != want {
			t.Errorf("RequiresLease(%s) = %v, want %v", state, got, want)
		}
	}
	for _, stray := range []core.State{"", "Releasing", "claimed"} {
		if RequiresLease(stray) {
			t.Errorf("%q is not a lifecycle state and is held by no run", stray)
		}
	}
}

func TestEveryStepOfARunMustCarryALease(t *testing.T) {
	t.Parallel()
	needing := 0
	for _, e := range Edges() {
		request := transitionOn(e.From, e.Event, e.To)
		if err := CheckLease(request); err != nil {
			t.Errorf("%q --%s--> %s under a lease was refused: %v", e.From, e.Event, e.To, err)
		}
		needsLease := RequiresLease(e.From) || e.Event == core.EventClaimed
		if !needsLease {
			if err := CheckLease(unleased(request)); err != nil {
				t.Errorf("%q --%s--> %s needs no lease but was refused without one: %v", e.From, e.Event, e.To, err)
			}
			continue
		}
		needing++
		hostile := map[string]core.Transition{"no lease at all": unleased(request)}
		ownerOnly := request
		ownerOnly.FencingToken = 0
		hostile["an owner with token zero"] = ownerOnly
		tokenOnly := request
		tokenOnly.LeaseOwner = ""
		hostile["a token with no owner"] = tokenOnly
		for name, write := range hostile {
			err := CheckLease(write)
			if !core.HasCode(err, core.CodeStaleFencingToken) {
				t.Errorf("%q --%s--> %s with %s: want %s, got %v", e.From, e.Event, e.To, name, core.CodeStaleFencingToken, err)
			}
		}
	}
	if needing != 10 {
		t.Fatalf("%d steps need a lease, want the claim and the nine steps out of a state a run holds", needing)
	}
}

func TestTheMachineRefusesAStepOfARunThatCarriesNoLease(t *testing.T) {
	t.Parallel()
	// With every guard satisfied, a worker that lost its lease and writes
	// with token zero is still stopped, on guarded and unguarded steps alike.
	machine := machineWith(passingGuards())
	checked := 0
	for _, e := range Edges() {
		if !RequiresLease(e.From) {
			continue
		}
		checked++
		err := machine.Check(context.Background(), unleased(transitionOn(e.From, e.Event, e.To)))
		if !core.HasCode(err, core.CodeStaleFencingToken) {
			t.Errorf("%s --%s--> %s without a lease: want %s, got %v", e.From, e.Event, e.To, core.CodeStaleFencingToken, err)
		}
	}
	if checked != 9 {
		t.Fatalf("%d steps leave a state a run holds, want 9", checked)
	}
}

func TestStepsOutsideARunNeedNoLease(t *testing.T) {
	t.Parallel()
	machine := machineWith(passingGuards())
	for _, e := range Edges() {
		if RequiresLease(e.From) || e.Event == core.EventClaimed {
			continue
		}
		if err := machine.Check(context.Background(), unleased(transitionOn(e.From, e.Event, e.To))); err != nil {
			t.Errorf("%q --%s--> %s without a lease was refused: %v", e.From, e.Event, e.To, err)
		}
	}
}

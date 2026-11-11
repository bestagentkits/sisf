package statemachine

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// The matrices below try every combination, legal or not, against the list of
// diagram edges in the fixtures. With every guard satisfied, the only thing
// that can refuse a step is the transition table itself.

func TestEveryStateAndEventPairLeadsOnlyWhereTheDiagramLeads(t *testing.T) {
	type cell struct {
		from  core.State
		event core.Event
	}
	want := map[cell]core.State{}
	for _, e := range briefEdges {
		want[cell{e.from, e.event}] = e.to
	}

	cells, legal := 0, 0
	for _, from := range sourceStates() {
		for _, event := range core.Events() {
			cells++
			next, err := Next(from, event)
			target, ok := want[cell{from, event}]
			if ok {
				legal++
				if err != nil || next != target {
					t.Errorf("Next(%q, %s) = %q, %v; want %s", from, event, next, err, target)
				}
				continue
			}
			if !core.HasCode(err, core.CodeInvalidTransition) || next != "" {
				t.Errorf("Next(%q, %s) = %q, %v; want %s", from, event, next, err, core.CodeInvalidTransition)
			}
		}
	}
	if cells != 16*23 || legal != 23 {
		t.Fatalf("checked %d cells with %d legal, want 368 cells with 23 legal", cells, legal)
	}
}

func TestOnlyDiagramEdgesAreAcceptedAcrossEverySourceTargetAndEvent(t *testing.T) {
	want := map[briefEdge]bool{}
	for _, e := range briefEdges {
		want[e] = true
	}
	machine := machineWith(passingGuards())

	tried, accepted := 0, 0
	for _, from := range sourceStates() {
		for _, to := range core.States() {
			for _, event := range core.Events() {
				tried++
				err := machine.Check(context.Background(), transitionOn(from, event, to))
				if want[briefEdge{from, event, to}] {
					accepted++
					if err != nil {
						t.Errorf("%q --%s--> %s is in the lifecycle but was refused: %v", from, event, to, err)
					}
					continue
				}
				if !core.HasCode(err, core.CodeInvalidTransition) {
					t.Errorf("%q --%s--> %s is not in the lifecycle; want %s, got %v",
						from, event, to, core.CodeInvalidTransition, err)
				}
			}
		}
	}
	if tried != 16*15*23 || accepted != 23 {
		t.Fatalf("tried %d steps and accepted %d, want 5520 tried and 23 accepted", tried, accepted)
	}
}

func TestALegalEventCannotBeRedirectedToAnotherState(t *testing.T) {
	machine := machineWith(passingGuards())
	for _, e := range briefEdges {
		for _, to := range core.States() {
			if to == e.to {
				continue
			}
			err := machine.Check(context.Background(), transitionOn(e.from, e.event, to))
			if !core.HasCode(err, core.CodeInvalidTransition) {
				t.Errorf("%q on %s was allowed to land in %s instead of %s: %v", e.from, e.event, to, e.to, err)
			}
		}
	}
}

func TestStatesAndTargetsOutsideTheLifecycleAreRefused(t *testing.T) {
	machine := machineWith(passingGuards())
	for _, e := range briefEdges {
		// "Releasing" appears in prose about prepaid platforms but is not one
		// of the fifteen states, so nothing may enter or leave it.
		for _, stray := range []core.State{"Releasing", "Done", ""} {
			into := machine.Check(context.Background(), transitionOn(e.from, e.event, stray))
			if !core.HasCode(into, core.CodeInvalidTransition) {
				t.Errorf("%q on %s entered %q: %v", e.from, e.event, stray, into)
			}
		}
		outOf := machine.Check(context.Background(), transitionOn("Releasing", e.event, e.to))
		if !core.HasCode(outOf, core.CodeInvalidTransition) {
			t.Errorf("an issue left the unknown state Releasing on %s: %v", e.event, outOf)
		}
	}
}

func TestARecordIsCreatedOnlyInIntake(t *testing.T) {
	machine := machineWith(passingGuards())
	if err := machine.Check(context.Background(), transitionOn("", core.EventIssueReceived, core.StateIntake)); err != nil {
		t.Fatalf("creating a record in Intake was refused: %v", err)
	}
	for _, from := range core.States() {
		err := machine.Check(context.Background(), transitionOn(from, core.EventIssueReceived, core.StateIntake))
		if !core.HasCode(err, core.CodeInvalidTransition) {
			t.Errorf("an issue in %s was received again into Intake: %v", from, err)
		}
	}
}
